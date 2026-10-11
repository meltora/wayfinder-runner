package trustroot

import "fmt"

const (
	ChannelDevelopment = "development"
	ChannelDevelop     = "develop"
	ChannelStaging     = "staging"
	ChannelProduction  = "production"
)

func Channels() []string {
	return []string{ChannelDevelopment, ChannelDevelop, ChannelStaging, ChannelProduction}
}

func ReleaseChannel(daemonChannel string) (string, error) {
	switch daemonChannel {
	case "":
		return ChannelDevelopment, nil
	case ChannelDevelop, ChannelStaging, ChannelProduction:
		return daemonChannel, nil
	default:
		return "", fmt.Errorf("this build has an unknown release channel")
	}
}
