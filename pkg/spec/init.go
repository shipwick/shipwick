package spec

// validateInit checks `init`. It is a switch and nothing else: the init
// process is Docker's own, and which one that is belongs to the server.
func (r raw) validateInit(verr *ValidationError) bool {
	if r.Init == nil {
		return false
	}
	if r.Static.Dir != "" {
		verr.add("init", staticExclusiveMessage, "")
		return false
	}
	return *r.Init
}
