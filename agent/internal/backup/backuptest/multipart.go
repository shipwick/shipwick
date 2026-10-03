package backuptest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Multipart uploads, as the real service has them: an upload is created under
// a key, parts are put to it by number, and completing it — with the parts'
// ETags, in order — makes the object. Until then the bucket's listing does not
// show it, and aborting it is what removes the parts.

type upload struct {
	key   string
	parts map[int][]byte
}

// SetMinPartSize changes the size every part but the last must have, which is
// 5 MiB as on the real service unless a test says otherwise.
func (s *S3) SetMinPartSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minPartSize = n
}

// FailNextPuts makes the next n uploads — objects or parts — answer 500; the
// ones after them work again.
func (s *S3) FailNextPuts(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = n
}

// FailComplete makes completing a multipart upload fail, or stops doing so.
func (s *S3) FailComplete(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failComplete = fail
}

// FailAborts makes aborting a multipart upload answer 500, or stops doing so.
func (s *S3) FailAborts(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAborts = fail
}

// Uploads returns the multipart uploads in progress, sorted, each as its key
// and the number of parts it holds: "db/7/data.tar (3 parts)".
func (s *S3) Uploads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, u := range s.uploads {
		out = append(out, fmt.Sprintf("%s (%d parts)", u.key, len(u.parts)))
	}
	sort.Strings(out)
	return out
}

// LeaveUpload places a multipart upload with the given parts in the bucket
// and never completes it, as a writer that died in the middle would have. It
// returns the upload's id.
func (s *S3) LeaveUpload(key string, parts ...[]byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.newUpload(key)
	for i, p := range parts {
		u.parts[i+1] = p
	}
	return "upload-" + strconv.Itoa(s.nextUpload)
}

// ListUploadsByKeyOnly makes the listing of unfinished uploads answer the way
// MinIO does: with the uploads to exactly the key the prefix names, and with
// nothing for a prefix that is only the beginning of keys.
func (s *S3) ListUploadsByKeyOnly(only bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadsByKeyOnly = only
}

func (s *S3) newUpload(key string) *upload {
	s.nextUpload++
	u := &upload{key: key, parts: map[int][]byte{}}
	s.uploads["upload-"+strconv.Itoa(s.nextUpload)] = u
	return u
}

func (s *S3) createUpload(w http.ResponseWriter, key string) {
	s.newUpload(key)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>upload-%d</UploadId></InitiateMultipartUploadResult>", Bucket, xmlText(key), s.nextUpload)
}

func (s *S3) upload(w http.ResponseWriter, key, id string) *upload {
	u := s.uploads[id]
	if u == nil || u.key != key {
		fail(w, http.StatusNotFound, "NoSuchUpload", "The specified multipart upload does not exist.")
		return nil
	}
	return u
}

func (s *S3) putPart(w http.ResponseWriter, key, id, number string, body []byte) {
	u := s.upload(w, key, id)
	if u == nil {
		return
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || n > 10000 {
		fail(w, http.StatusBadRequest, "InvalidArgument", "Part number must be an integer between 1 and 10000, inclusive.")
		return
	}
	u.parts[n] = body
	w.Header().Set("ETag", etag(body))
	w.WriteHeader(http.StatusOK)
}

func etag(part []byte) string {
	sum := md5.Sum(part)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *S3) completeUpload(w http.ResponseWriter, key, id string, body []byte) {
	u := s.upload(w, key, id)
	if u == nil {
		return
	}
	var list struct {
		Part []struct {
			PartNumber int
			ETag       string
		}
	}
	if err := xml.Unmarshal(body, &list); err != nil || len(list.Part) == 0 {
		fail(w, http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed.")
		return
	}
	if s.failComplete {
		// The real service has answered 200 by the time it finds out.
		fail(w, http.StatusOK, "InternalError", "We encountered an internal error. Please try again.")
		return
	}
	var object []byte
	for i, p := range list.Part {
		part, ok := u.parts[p.PartNumber]
		switch {
		case i > 0 && p.PartNumber <= list.Part[i-1].PartNumber:
			fail(w, http.StatusBadRequest, "InvalidPartOrder", "The list of parts was not in ascending order.")
			return
		case !ok || p.ETag != etag(part):
			fail(w, http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found.")
			return
		case i < len(list.Part)-1 && len(part) < s.minPartSize:
			fail(w, http.StatusBadRequest, "EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.")
			return
		}
		object = append(object, part...)
	}
	s.objects[key] = object
	s.modified[key] = time.Now()
	delete(s.uploads, id)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, "<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key></CompleteMultipartUploadResult>", Bucket, xmlText(key))
}

func (s *S3) abortUpload(w http.ResponseWriter, key, id string) {
	if s.failAborts {
		fail(w, http.StatusInternalServerError, "InternalError", "We encountered an internal error.")
		return
	}
	if s.upload(w, key, id) == nil {
		return
	}
	delete(s.uploads, id)
	w.WriteHeader(http.StatusNoContent)
}

// listUploads answers ListMultipartUploads, in pages like the listing of
// objects.
func (s *S3) listUploads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	type entry struct {
		Key      string
		UploadId string
	}
	var entries []entry
	for id, u := range s.uploads {
		if u.key == q.Get("prefix") || (!s.uploadsByKeyOnly && strings.HasPrefix(u.key, q.Get("prefix"))) {
			entries = append(entries, entry{Key: u.key, UploadId: id})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Key != entries[j].Key {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].UploadId < entries[j].UploadId
	})
	if key, id := q.Get("key-marker"), q.Get("upload-id-marker"); key != "" {
		i := sort.Search(len(entries), func(i int) bool {
			return entries[i].Key > key || (entries[i].Key == key && entries[i].UploadId > id)
		})
		entries = entries[i:]
	}
	size := s.pageSize
	if size == 0 {
		size = 1000
	}
	var page struct {
		XMLName            xml.Name `xml:"ListMultipartUploadsResult"`
		IsTruncated        bool
		NextKeyMarker      string `xml:",omitempty"`
		NextUploadIdMarker string `xml:",omitempty"`
		Upload             []entry
	}
	if len(entries) > size {
		entries = entries[:size]
		page.IsTruncated, page.NextKeyMarker, page.NextUploadIdMarker = true, entries[size-1].Key, entries[size-1].UploadId
	}
	page.Upload = entries
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(page)
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
