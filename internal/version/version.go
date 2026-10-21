// Package version says which build of Talaria this is.
package version

import "runtime/debug"

// Version is set at build time: -ldflags "-X talaria/internal/version.Version=v1.2.3".
// A build made without it reports "dev", plus the commit when Go recorded one.
var Version = "dev"

// String is the version, with the commit it was built from when known.
func String() string {
	v := Version
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "+dirty"
				}
			}
		}
		if rev != "" && v == "dev" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			v += " (" + rev + dirty + ")"
		}
	}
	return v
}
