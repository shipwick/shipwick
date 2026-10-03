package deploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

var (
	// ErrVolumeNotFound means the active deployment mounts no volume by that name.
	ErrVolumeNotFound = errors.New("the application has no volume by that name")
	// ErrNotStopped means a restore was asked of an application that runs.
	ErrNotStopped = errors.New("the application is running; stop it first with: shipwick stop")
	// ErrInvalidArchive means the uploaded body is not a tar archive.
	ErrInvalidArchive = errors.New("the archive is not a tar file")
)

// volumeRemover is what a restore needs beyond the Runtime interface. The
// production runtime and the test fake both provide it; the interface itself
// is not widened for an operation that only one path uses.
type volumeRemover interface {
	RemoveVolume(ctx context.Context, app, volume string) error
}

// Volumes lists the volumes of the application's active deployment.
func (e *Engine) Volumes(ctx context.Context, name string) ([]api.Volume, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	if app.ActiveDeploymentID == nil {
		return nil, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Volume, 0, len(d.Spec.Volumes))
	for _, v := range d.Spec.Volumes {
		out = append(out, api.Volume{Name: v.Name, Path: v.Path})
	}
	return out, nil
}

// Backup streams a tar archive of a volume's contents, read through the
// replica that mounts it — running or stopped. The application stays locked
// until the stream is closed, so that no deployment replaces the container
// half-way through; a deployment asked for meanwhile is told ErrBusy.
//
// A running application may be writing: the copy of a database that is being
// written to is not guaranteed consistent. That is the caller's call to make;
// the CLI says so.
func (e *Engine) Backup(ctx context.Context, name, volume string) (io.ReadCloser, error) {
	if err := e.lock(ctx, name); err != nil {
		return nil, err
	}
	d, v, replicas, err := e.volumeTarget(ctx, name, volume)
	if err != nil {
		e.unlock(name)
		return nil, err
	}
	if len(replicas) == 0 {
		e.unlock(name)
		return nil, fmt.Errorf("%s has no container to read the volume through; deploy it again", d.Application)
	}
	rc, err := e.rt.ExportPath(ctx, replicas[0].ContainerID, v.Path)
	if err != nil {
		e.unlock(name)
		return nil, err
	}
	e.log.Info("volume backup started", "app", name, "volume", volume)
	return &lockedStream{ReadCloser: rc, release: func() { e.unlock(name) }}, nil
}

// lockedStream holds the application lock for as long as it is open.
type lockedStream struct {
	io.ReadCloser
	release func()
}

func (s *lockedStream) Close() error {
	err := s.ReadCloser.Close()
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return err
}

// Restore replaces a volume's contents with a tar archive. The application
// must be stopped: its volume is removed with everything in it, the replica
// is created again — which creates the volume anew, empty — and the archive
// is extracted into the mount point before the process ever runs. The
// application stays stopped afterwards; `shipwick start` brings it back.
//
// The archive is checked for a tar header before anything is removed. What it
// contains is not: a database that does not like what it finds is the
// database's to complain about, in its logs.
func (e *Engine) Restore(ctx context.Context, name, volume string, archive io.Reader) error {
	remover, ok := e.rt.(volumeRemover)
	if !ok {
		return errors.New("this runtime cannot remove volumes")
	}
	peek := bufio.NewReader(archive)
	head, err := peek.Peek(512)
	if err != nil {
		return ErrInvalidArchive
	}
	if _, err := tar.NewReader(bytes.NewReader(head)).Next(); err != nil {
		return ErrInvalidArchive
	}

	if err := e.lock(ctx, name); err != nil {
		return err
	}
	defer e.unlock(name)

	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return err
	}
	d, v, replicas, err := e.volumeTarget(ctx, name, volume)
	if err != nil {
		return err
	}
	if app.DesiredState != api.DesiredStopped {
		return ErrNotStopped
	}
	for _, r := range replicas {
		if c, err := e.rt.InspectContainer(ctx, r.ContainerID); err == nil && c.Running {
			return ErrNotStopped
		}
	}

	// Removing the containers is what frees the volume; the replica rows go
	// with them, so that a start in between finds nothing to start.
	for _, r := range replicas {
		if err := e.retireContainer(ctx, r.ContainerID, e.gracePeriod(d.Spec)); err != nil {
			return fmt.Errorf("remove replica %d: %w", r.Index, err)
		}
	}
	if err := remover.RemoveVolume(ctx, name, volume); err != nil {
		return err
	}
	indexes := make([]int, 0, d.Spec.Replicas)
	for i := 1; i <= d.Spec.Replicas; i++ {
		indexes = append(indexes, i)
	}
	created, err := e.createReplicas(ctx, d, indexes)
	if err != nil {
		return fmt.Errorf("the volume was emptied but its container could not be created again: %w; deploy the application again", err)
	}

	counted := &countingReader{r: peek}
	if err := e.rt.ImportPath(ctx, created[0].ContainerID, v.Path, counted); err != nil {
		e.appEvent(ctx, app, fmt.Sprintf("Restore of volume %s failed after %s: %v", volume, spec.FormatMemory(counted.n), err))
		return fmt.Errorf("the volume was emptied but the archive could not be extracted into it: %w; upload it again", err)
	}
	e.appEvent(ctx, app, fmt.Sprintf("Volume %s restored from a backup (%s)", volume, spec.FormatMemory(counted.n)))
	return nil
}

// volumeTarget resolves what a backup or restore works on: the active
// deployment, the volume as it mounts it, and its replicas.
func (e *Engine) volumeTarget(ctx context.Context, name, volume string) (store.Deployment, spec.Volume, []store.Replica, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return store.Deployment{}, spec.Volume{}, nil, err
	}
	if app.ActiveDeploymentID == nil {
		return store.Deployment{}, spec.Volume{}, nil, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return store.Deployment{}, spec.Volume{}, nil, err
	}
	for _, v := range d.Spec.Volumes {
		if v.Name != volume {
			continue
		}
		replicas, err := e.store.ListReplicas(ctx, d.ID)
		if err != nil {
			return store.Deployment{}, spec.Volume{}, nil, err
		}
		return d, v, replicas, nil
	}
	return store.Deployment{}, spec.Volume{}, nil, ErrVolumeNotFound
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
