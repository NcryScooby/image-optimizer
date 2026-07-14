package folder

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	safeSegment     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	legacyKindSeg   = regexp.MustCompile(`^(catalog|avatars|themes|admins)$`)
	shopKindPattern = regexp.MustCompile(`^storely/[0-9]+/(catalog|avatars)$`)
	themePattern    = regexp.MustCompile(`^storely/[0-9]+/themes/[a-zA-Z0-9_-]+$`)
	panelPattern    = regexp.MustCompile(`^storely/panel/admins/[0-9]+$`)
)

func Validate(raw string) (string, error) {
	folder := strings.Trim(strings.ReplaceAll(raw, "\\", "/"), "/")
	if folder == "" {
		return "", fmt.Errorf("folder is required")
	}
	if strings.Contains(folder, "//") {
		return "", fmt.Errorf("invalid folder path")
	}
	cleaned := path.Clean(folder)
	if cleaned != folder || strings.HasPrefix(cleaned, "..") || cleaned == "." {
		return "", fmt.Errorf("invalid folder path")
	}
	if !strings.HasPrefix(folder, "storely/") {
		return "", fmt.Errorf("folder must start with storely/")
	}

	parts := strings.Split(folder, "/")
	for _, p := range parts {
		if p == "" || !safeSegment.MatchString(p) {
			return "", fmt.Errorf("invalid folder segment")
		}
	}

	if shopKindPattern.MatchString(folder) || themePattern.MatchString(folder) || panelPattern.MatchString(folder) {
		return folder, nil
	}

	hasKnownKind := false
	for _, p := range parts[1:] {
		if legacyKindSeg.MatchString(p) {
			hasKnownKind = true
			break
		}
	}
	if !hasKnownKind {
		return "", fmt.Errorf("folder must include a known media kind")
	}
	return folder, nil
}
