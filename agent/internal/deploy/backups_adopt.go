package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Backups the database has forgotten. A server whose state was restored from
// yesterday's backup of it does not know the backups taken since; their files
// are in the directory and in the bucket all the same, under ids the restored
// database will not hand out again (prepareBackups sees to that). Adopting
// them is writing the records back from what the files say: whose they are,
// which volumes, how large, when, encrypted or not. What the files do not say
// — whether it was the schedule or a person, whether it was ever verified —
// stays unsaid: an adopted backup has the trigger "adopted" and no
// verification.

// archiveSuffix is what a volume's archive is named after the volume.
const archiveSuffix = ".tar"

// AdoptBackups records the backups the destinations hold and the database
// does not know: those of one application, or with an empty name everything —
// every application's, the agent's own state, the exports. It changes no
// file. A run that cannot be a complete backup is reported and left alone.
func (e *Engine) AdoptBackups(ctx context.Context, application string) (api.BackupAdoption, error) {
	out := api.BackupAdoption{Adopted: []api.AdoptedBackup{}, Skipped: []api.SkippedBackup{}}
	storage := e.opts.Backups.Storage
	if storage == nil {
		return out, ErrBackupsDisabled
	}
	if application != "" {
		if err := spec.ValidateName(application); err != nil {
			return out, err
		}
	}
	// A bucket that another installation writes to holds that installation's
	// backups, under ids that mean something else here.
	if err := e.prepareBackups(ctx); err != nil {
		return out, err
	}
	// The ids first, the files second. The other way round, a backup that was
	// pruned in between would be found by its files and adopted for having no
	// record; this way round, one that started in between has files and an id
	// the list does not know — and a record, which the insert below respects.
	known, err := e.store.BackupRunIDs(ctx)
	if err != nil {
		return out, err
	}
	found, err := storage.Found(ctx, application)
	if err != nil {
		return out, err
	}
	now := time.Now()
	for _, f := range found {
		kind, ours := backupKind(f.Owner)
		if !ours || known[f.ID] {
			continue
		}
		owner := ""
		if kind == api.BackupKindApplication {
			owner = f.Owner
		}
		run, reason := adoptable(f, kind, now)
		switch {
		case reason != "":
			out.Skipped = append(out.Skipped, api.SkippedBackup{Kind: kind, Application: owner, ID: f.ID, Reason: reason})
			continue
		case len(run.Volumes) == 0:
			continue // a directory of that name, and nothing in it that is a backup's
		}
		adopted, err := e.store.AdoptBackupRun(ctx, run)
		if err != nil {
			return out, err
		}
		if adopted {
			out.Adopted = append(out.Adopted, api.AdoptedBackup{Kind: kind, Application: owner, Backup: backupView(run)})
		}
	}
	if len(out.Adopted)+len(out.Skipped) > 0 {
		e.log.Info("backups adopted", "adopted", len(out.Adopted), "skipped", len(out.Skipped), "by", actorFrom(ctx))
	}
	return out, nil
}

// backupKind says what the backups kept under owner are backups of; false for
// a name nothing of the agent's is kept under.
func backupKind(owner string) (string, bool) {
	switch {
	case owner == backup.StateOwner:
		return api.BackupKindState, true
	case owner == ExportOwner:
		return api.BackupKindExport, true
	case spec.ValidateName(owner) == nil:
		return api.BackupKindApplication, true
	}
	return "", false
}

// adoptable turns the files of a run into the record of the backup they are,
// or says why they are not one. A record without volumes, and no reason,
// means that none of the files is a backup's.
func adoptable(f backup.FoundRun, kind string, now time.Time) (store.BackupRun, string) {
	at := f.Modified.UTC()
	if at.IsZero() {
		at = now.UTC()
	}
	run := store.BackupRun{ID: f.ID, Application: f.Owner, Trigger: api.BackupTriggerAdopted, Status: api.BackupSucceeded,
		Volumes: []api.BackupVolume{}, Destinations: f.Destinations, StartedAt: at, CompletedAt: &at}

	// What a backup of the agent's state or an export consists of is fixed; an
	// application's volumes are whatever archives are there.
	var wanted []string
	switch kind {
	case api.BackupKindState:
		wanted = []string{stateDatabaseFile, stateKeyFile}
	case api.BackupKindExport:
		wanted = []string{exportFile}
	}
	files := map[string]backup.FoundFile{}
	var names []string
	for _, file := range f.Files {
		volume, isArchive := strings.CutSuffix(file.Name, archiveSuffix)
		if kind == api.BackupKindApplication && (!isArchive || spec.ValidateName(volume) != nil) {
			continue
		}
		if _, twice := files[file.Name]; twice {
			return run, fmt.Sprintf("%s is there twice, encrypted and not", file.Name)
		}
		files[file.Name] = file
		if kind == api.BackupKindApplication {
			names = append(names, file.Name)
		}
	}
	for _, name := range wanted {
		if _, ok := files[name]; !ok {
			return run, fmt.Sprintf("%s is missing: the backup was not finished", name+".enc")
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return run, ""
	}

	for i, name := range names {
		file := files[name]
		stored := name
		if file.Encrypted {
			stored += ".enc"
		}
		size := file.Size
		switch {
		case i > 0 && file.Encrypted != run.Encrypted:
			return run, "some of its files are encrypted and some are not"
		case len(wanted) > 0 && !file.Encrypted:
			return run, fmt.Sprintf("%s is not encrypted, and the agent writes it encrypted only", stored)
		case file.Differs:
			return run, fmt.Sprintf("%s has one size in the directory and another in the bucket", stored)
		case file.Encrypted:
			plain, ok := backupfile.PlainSize(file.Size)
			if !ok {
				return run, fmt.Sprintf("%s is not a whole encrypted backup: no file the agent writes has %d bytes", stored, file.Size)
			}
			size = plain
		}
		run.Encrypted = file.Encrypted
		volume := name
		if kind == api.BackupKindApplication {
			volume = strings.TrimSuffix(name, archiveSuffix)
		}
		run.Volumes = append(run.Volumes, api.BackupVolume{Volume: volume, SizeBytes: size})
	}
	if len(run.Destinations) == 0 {
		return run, "its files are spread over the directory and the bucket, and neither holds all of them"
	}
	return run, ""
}
