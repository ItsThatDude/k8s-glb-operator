package version

import (
	"fmt"

	"github.com/blang/semver/v4"
)

var (
	Version = "0.1.0"
)

func GetMajorAndMinorVersion() (string, error) {
	semverVersion, err := semver.Parse(Version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d", semverVersion.Major, semverVersion.Minor), nil
}
