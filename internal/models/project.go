package models

// ProjectDetails contains essential GitLab project information for frontend display
type ProjectDetails struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	NameWithNamespace string     `json:"nameWithNamespace"`
	Path              string     `json:"path"`
	PathWithNamespace string     `json:"pathWithNamespace"`
	Description       string     `json:"description"`
	WebURL            string     `json:"webUrl"`
	AvatarURL         string     `json:"avatarUrl,omitempty"`
	DefaultBranch     string     `json:"defaultBranch,omitempty"`
	Visibility        string     `json:"visibility"`
	Namespace         *Namespace `json:"namespace,omitempty"`
}

// Namespace represents the project namespace
type Namespace struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"` // "user" or "group"
	AvatarURL string `json:"avatarUrl,omitempty"`
	WebURL    string `json:"webUrl,omitempty"`
}
