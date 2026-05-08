package version

const (
	Name    = "beddel-desk"
	Version = "0.2.0"
)

func String() string {
	return Name + " " + Version
}

