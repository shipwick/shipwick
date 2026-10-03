package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// An object too large for one PUT — 5 GB on S3 and the services modelled on
// it — goes up as a multipart upload: created, sent part by part, completed.
// Until it is completed the object does not exist, which is what a backup
// wants of a half-sent archive; the parts do exist, and are charged for, until
// the upload is completed or aborted. Hence the two rules of this file: an
// upload that does not complete is aborted by whoever started it, and an
// agent aborts, before its own first upload, what an agent before it did not
// live to abort (Storage.AbortLeftovers).

const (
	// defaultPartSize is the size of a part, and the size up to which an
	// object goes in a single PUT. Parts are read from the file they come
	// from, so the size costs no memory; it is what one failed request has to
	// send again.
	defaultPartSize = 64 << 20
	// maxParts is how many parts the service takes for one object, and
	// maxObjectBytes the largest object it holds: 5 TiB.
	maxParts       = 10000
	maxObjectBytes = 5 << 40
	// partAttempts is how often a part is sent before the upload is given up,
	// defaultRetryPause how long is waited in between. An archive of a hundred
	// gigabytes is more than a thousand requests, and one reset connection
	// among them should not cost the backup.
	partAttempts      = 3
	defaultRetryPause = 2 * time.Second
	// abortTimeout bounds the request that aborts an upload, which is made
	// when the context of the upload itself may already be over.
	abortTimeout = 30 * time.Second
)

// errPartsRemain marks the failure of an upload whose parts are still in the
// bucket, because aborting it failed as well.
var errPartsRemain = errors.New("the parts already sent could not be removed from the bucket")

// Upload stores the size bytes of src under key: in one PUT when they fit a
// part, as a multipart upload otherwise. sha256Hex is the SHA-256 of all of
// it, which a single PUT signs; parts are hashed as they are sent. An upload
// that fails, or whose context ends, is aborted, so that nothing of it stays
// in the bucket.
//
// begun, when it is not nil, is told the id of a multipart upload before its
// first part is sent: whoever notes it down can abort the upload when this
// process does not live to (abortUpload).
func (s *S3) Upload(ctx context.Context, key string, src io.ReaderAt, size int64, sha256Hex string, begun func(uploadID string) error) error {
	if size <= s.partSize {
		// NopCloser: the transport closes a body it is given, and whose file
		// this is has more to do with it.
		body := io.NopCloser(&contextReader{ctx: ctx, r: io.NewSectionReader(src, 0, size)})
		return s.Put(ctx, key, body, size, sha256Hex)
	}
	if size > maxObjectBytes {
		return fmt.Errorf("upload %s: the archive is larger than %d TiB, the largest object a bucket holds", key, maxObjectBytes>>40)
	}
	partSize := partSizeFor(size, s.partSize)

	id, err := s.createUpload(ctx, key)
	if err != nil {
		return err
	}
	if begun != nil {
		err = begun(id)
	}
	if err == nil {
		err = s.sendParts(ctx, key, id, src, size, partSize)
	}
	if err == nil {
		return nil
	}
	// The request that failed may have been cut by the context; the abort
	// must not be.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	if aerr := s.abortUpload(actx, key, id); aerr != nil {
		return fmt.Errorf("%w; %w (%v): the agent tries again when it next starts", err, errPartsRemain, aerr)
	}
	return err
}

// partSizeFor is the size of the parts an object of size bytes goes up in:
// the usual one, or what keeps a very large object within the number of parts
// the service takes, in whole MiB.
func partSizeFor(size, usual int64) int64 {
	if size <= usual*maxParts {
		return usual
	}
	return (size/maxParts + 1<<20) &^ (1<<20 - 1)
}

// sendParts sends every part of an upload and completes it.
func (s *S3) sendParts(ctx context.Context, key, id string, src io.ReaderAt, size, partSize int64) error {
	var etags []string
	for offset := int64(0); offset < size; offset += partSize {
		length := min(partSize, size-offset)
		etag, err := s.sendPart(ctx, key, id, len(etags)+1, src, offset, length)
		if err != nil {
			return err
		}
		etags = append(etags, etag)
	}
	return s.completeUpload(ctx, key, id, etags)
}

func (s *S3) createUpload(ctx context.Context, key string) (string, error) {
	req, err := s.request(ctx, http.MethodPost, s.key(key), url.Values{"uploads": {""}}, nil, emptyPayload)
	if err != nil {
		return "", err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", key, redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload %s: %w", key, responseError(resp))
	}
	var created struct {
		UploadId string
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil || created.UploadId == "" {
		return "", fmt.Errorf("upload %s: the bucket did not answer with a multipart upload", key)
	}
	return created.UploadId, nil
}

// sendPart sends one part, more than once if it has to, and returns the ETag
// the service gave it, which completing the upload quotes back.
func (s *S3) sendPart(ctx context.Context, key, id string, number int, src io.ReaderAt, offset, length int64) (string, error) {
	// Read once for the hash the signature covers and once for the request:
	// the second read comes from the page cache, and no part is ever held in
	// memory.
	hash := sha256.New()
	if _, err := io.Copy(hash, &contextReader{ctx: ctx, r: io.NewSectionReader(src, offset, length)}); err != nil {
		return "", fmt.Errorf("upload %s: read part %d: %w", key, number, err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	query := url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {id}}

	var last error
	for attempt := 1; attempt <= partAttempts; attempt++ {
		if attempt > 1 {
			pause := time.NewTimer(s.retryPause)
			select {
			case <-pause.C:
			case <-ctx.Done():
				pause.Stop()
				return "", fmt.Errorf("upload %s: part %d: %w", key, number, ctx.Err())
			}
		}
		body := func() io.ReadCloser {
			return io.NopCloser(&contextReader{ctx: ctx, r: io.NewSectionReader(src, offset, length)})
		}
		req, err := s.request(ctx, http.MethodPut, s.key(key), query, body(), sum)
		if err != nil {
			return "", err
		}
		req.ContentLength = length
		// What lets the transport send the part again by itself over a
		// connection the service had closed while it sat idle.
		req.GetBody = func() (io.ReadCloser, error) { return body(), nil }
		resp, err := s.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("upload %s: part %d: %w", key, number, ctx.Err())
			}
			last = redactURL(err)
			continue
		}
		etag := resp.Header.Get("ETag")
		if resp.StatusCode == http.StatusOK && etag != "" {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return etag, nil
		}
		last = responseError(resp)
		if resp.StatusCode == http.StatusOK {
			last = fmt.Errorf("the bucket accepted the part without naming it (no ETag)")
		}
		resp.Body.Close()
		// A refusal is not going to change its mind: the credentials, the
		// part's size, an upload that is gone.
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}
	return "", fmt.Errorf("upload %s: part %d: %w", key, number, last)
}

func (s *S3) completeUpload(ctx context.Context, key, id string, etags []string) error {
	type part struct {
		PartNumber int
		ETag       string
	}
	list := struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Part    []part
	}{}
	for i, etag := range etags {
		list.Part = append(list.Part, part{PartNumber: i + 1, ETag: etag})
	}
	body, err := xml.Marshal(list)
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	sum := sha256.Sum256(body)
	req, err := s.request(ctx, http.MethodPost, s.key(key), url.Values{"uploadId": {id}}, bytes.NewReader(body), hex.EncodeToString(sum[:]))
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: complete: %w", key, redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload %s: complete: %w", key, responseError(resp))
	}
	// Putting the parts together can take minutes, and the service answers
	// 200 before it has: a failure then arrives as an error document in the
	// body of a successful response.
	var result struct {
		XMLName xml.Name
		Code    string
		Message string
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("upload %s: complete: %w", key, err)
	}
	if xml.Unmarshal(data, &result) == nil && result.XMLName.Local == "Error" {
		return fmt.Errorf("upload %s: complete: the bucket answered %s: %s", key, result.Code, strings.TrimSuffix(result.Message, "."))
	}
	return nil
}

// abortUpload removes an upload and its parts. One that is not there — it was
// completed after all, or aborted before — is not an error.
func (s *S3) abortUpload(ctx context.Context, key, id string) error {
	req, err := s.request(ctx, http.MethodDelete, s.key(key), url.Values{"uploadId": {id}}, nil, emptyPayload)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("abort the upload of %s: %w", key, redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("abort the upload of %s: %w", key, responseError(resp))
	}
	return nil
}

// pendingUpload is a multipart upload that was started and neither completed
// nor aborted.
type pendingUpload struct {
	Key string // without the configured prefix
	ID  string
}

// listUploads returns the multipart uploads in progress whose key starts with
// under, following the service's pagination to the end. Not every service
// answers this alike: S3 lists by prefix, MinIO only what is under exactly
// that key.
func (s *S3) listUploads(ctx context.Context, under string) ([]pendingUpload, error) {
	var out []pendingUpload
	keyMarker, idMarker := "", ""
	for {
		q := url.Values{"uploads": {""}}
		if prefix := s.key(under); prefix != "" {
			q.Set("prefix", prefix)
		}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
			q.Set("upload-id-marker", idMarker)
		}
		req, err := s.request(ctx, http.MethodGet, "", q, nil, emptyPayload)
		if err != nil {
			return nil, err
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list unfinished uploads: %w", redactURL(err))
		}
		if resp.StatusCode != http.StatusOK {
			err := responseError(resp)
			resp.Body.Close()
			return nil, fmt.Errorf("list unfinished uploads: %w", err)
		}
		var page struct {
			IsTruncated        bool
			NextKeyMarker      string
			NextUploadIdMarker string
			Upload             []struct {
				Key      string
				UploadId string
			}
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list unfinished uploads: the answer is not a listing: %w", err)
		}
		for _, u := range page.Upload {
			key := u.Key
			if s.cfg.Prefix != "" {
				key = strings.TrimPrefix(key, s.cfg.Prefix+"/")
			}
			out = append(out, pendingUpload{Key: key, ID: u.UploadId})
		}
		if !page.IsTruncated || page.NextKeyMarker == "" {
			return out, nil
		}
		keyMarker, idMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}

// uploadSuffix ends the name of the note Write leaves next to a file while it
// goes to the bucket in parts: <file>.upload, holding the upload's id.
const uploadSuffix = ".upload"

// AbortLeftovers aborts the multipart uploads an earlier run of the agent
// started and did not finish — it was killed, or the server lost power, in
// the middle of an archive — and returns how many there were. Their parts are
// invisible in a listing of the bucket and charged for all the same.
//
// It knows them two ways. The notes Write left in the directory name each
// upload exactly, and work with every service. A server whose disk went with
// the agent has no notes, so the bucket is asked as well, for the uploads
// under this installation's prefix; of those, only uploads to keys shaped
// like a run's files are touched, because whatever else goes on in a bucket
// shared without a prefix is not this installation's to end.
//
// Call it only once the bucket is known to be this installation's (Prepare),
// and while no upload of this agent is in progress: it cannot tell those from
// leftovers.
func (s *Storage) AbortLeftovers(ctx context.Context) (int, error) {
	if s.s3 == nil {
		return 0, nil
	}
	aborted := 0
	var errs []error
	owners, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("read backup directory: %w", err))
	}
	for _, owner := range owners {
		if !owner.IsDir() || !plainName(owner.Name()) {
			continue
		}
		runs, _ := os.ReadDir(filepath.Join(s.dir, owner.Name()))
		for _, run := range runs {
			id, err := strconv.ParseInt(run.Name(), 10, 64)
			if err != nil || id < 1 || !run.IsDir() {
				continue
			}
			n, err := s.abortNoted(ctx, owner.Name(), id)
			aborted += n
			if err != nil {
				errs = append(errs, err)
				continue
			}
			// A run that held nothing but the note goes with it; one that
			// holds files is not empty, and the error is the answer.
			if n > 0 {
				os.Remove(s.runDir(owner.Name(), id))
				os.Remove(filepath.Join(s.dir, owner.Name()))
			}
		}
	}

	uploads, err := s.s3.listUploads(ctx, "")
	if err != nil {
		return aborted, errors.Join(append(errs, err)...)
	}
	for _, u := range uploads {
		if _, _, _, ok := splitRunKey(u.Key); !ok {
			continue
		}
		if err := s.s3.abortUpload(ctx, u.Key, u.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		aborted++
	}
	return aborted, errors.Join(errs...)
}

// abortNoted aborts the uploads noted in a run's directory and removes the
// notes of those it aborted. It returns how many that were.
func (s *Storage) abortNoted(ctx context.Context, owner string, run int64) (int, error) {
	dir := s.runDir(owner, run)
	entries, err := os.ReadDir(dir)
	if err != nil || s.s3 == nil {
		return 0, nil // no directory, no notes; no bucket, nothing to abort in
	}
	aborted := 0
	var errs []error
	for _, e := range entries {
		file, ok := strings.CutSuffix(e.Name(), uploadSuffix)
		if !ok || !plainName(file) {
			continue
		}
		note := filepath.Join(dir, e.Name())
		id, err := os.ReadFile(note)
		if err != nil {
			errs = append(errs, fmt.Errorf("read the note of an unfinished upload: %w", err))
			continue
		}
		// An empty note was cut off before the id was in it; there is nothing
		// to abort by, and the bucket's own list is what is left.
		if uploadID := strings.TrimSpace(string(id)); uploadID != "" {
			if err := s.s3.abortUpload(ctx, runKey(owner, run)+file, uploadID); err != nil {
				errs = append(errs, err)
				continue
			}
			aborted++
		}
		os.Remove(note)
	}
	return aborted, errors.Join(errs...)
}

// splitRunKey takes a key of the form <owner>/<run id>/<file> apart.
func splitRunKey(key string) (owner string, run int64, file string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", 0, "", false
	}
	run, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || run < 1 {
		return "", 0, "", false
	}
	return parts[0], run, parts[2], true
}
