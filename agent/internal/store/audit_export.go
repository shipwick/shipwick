package store

import (
	"context"

	"github.com/shipwick/shipwick/pkg/api"
)

// auditExportPage is how many entries EachAuditEntry reads at a time.
const auditExportPage = 500

// EachAuditEntry hands fn every entry that matches, newest first, and stops
// at the first error fn returns. f.Limit is not looked at; f.Before, when
// set, is where it starts.
//
// The trail is read a page at a time, each page a query of its own that is
// over before fn sees its entries. A cursor held open for as long as the
// reader of an export takes would hold the database's one connection with
// it, and every request behind it. What is written while an export runs is
// newer than where it started and is not part of it.
func (s *Store) EachAuditEntry(ctx context.Context, f AuditFilter, fn func(api.AuditEntry) error) error {
	f.Limit = auditExportPage
	for {
		page, err := s.AuditEntries(ctx, f)
		if err != nil {
			return err
		}
		for _, e := range page {
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(page) < auditExportPage {
			return nil
		}
		f.Before = page[len(page)-1].ID
	}
}
