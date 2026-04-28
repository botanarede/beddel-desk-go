package version

const (
	Name    = "beddel-desk"
	Version = "0.1.0-dev"
)

func String() string {
	return Name + " " + Version
}

