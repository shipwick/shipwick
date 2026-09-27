package commands

import "os"

// replaceExecutable puts staged in place of current. Windows lets a running
// executable be renamed but neither overwritten nor deleted, so the old one
// moves aside and the next run removes it.
func replaceExecutable(current, staged string) error {
	old := staleExecutableName(current)
	os.Remove(old)
	if err := os.Rename(current, old); err != nil {
		return err
	}
	if err := os.Rename(staged, current); err != nil {
		os.Rename(old, current)
		return err
	}
	return nil
}

// removeStaleExecutable deletes what the previous upgrade left behind. Best
// effort: another shipwick may still be running it.
func removeStaleExecutable() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	os.Remove(staleExecutableName(exe))
}
