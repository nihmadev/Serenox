package v1

const ProfilePermission = "telegram.profile.write"

type ProfileOperation string

const (
	ProfileAcquire ProfileOperation = "acquire"
	ProfileUpdate  ProfileOperation = "update"
	ProfileRestore ProfileOperation = "restore"
	ProfileRelease ProfileOperation = "release"
)

type ProfileNameAction struct {
	Operation  ProfileOperation `json:"operation"`
	FirstName  string           `json:"first_name,omitempty"`
	LastName   string           `json:"last_name,omitempty"`
	Generation uint64           `json:"generation,omitempty"`
	LeaseToken string           `json:"lease_token,omitempty"`
}

type ProfileResult struct {
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	Generation uint64 `json:"generation,omitempty"`
	LeaseToken string `json:"lease_token,omitempty"`
	Released   bool   `json:"released,omitempty"`
}

func (a ProfileNameAction) Validate() error {
	if a.Operation != ProfileAcquire && a.Operation != ProfileUpdate && a.Operation != ProfileRestore && a.Operation != ProfileRelease {
		return invalid("profile.operation", "unknown operation %q", a.Operation)
	}
	if err := validateProfileName("profile.first_name", a.FirstName, a.Operation == ProfileUpdate); err != nil {
		return err
	}
	if err := validateProfileName("profile.last_name", a.LastName, false); err != nil {
		return err
	}
	if a.Operation == ProfileAcquire {
		if a.FirstName != "" || a.LastName != "" || a.Generation != 0 || a.LeaseToken != "" {
			return invalid("profile.acquire", "must not contain names or lease identity")
		}
		return nil
	}
	if a.Generation == 0 {
		return invalid("profile.generation", "is required")
	}
	if err := validString("profile.lease_token", a.LeaseToken, true); err != nil {
		return err
	}
	if len(a.LeaseToken) > 128 {
		return invalid("profile.lease_token", "exceeds 128 bytes")
	}
	if (a.Operation == ProfileRestore || a.Operation == ProfileRelease) && (a.FirstName != "" || a.LastName != "") {
		return invalid("profile.restore", "restore and release use the core-owned original name")
	}
	return nil
}

func (r ProfileResult) Validate() error {
	if err := validateProfileName("profile_result.first_name", r.FirstName, true); err != nil {
		return err
	}
	if err := validateProfileName("profile_result.last_name", r.LastName, false); err != nil {
		return err
	}
	if r.Generation != 0 && r.LeaseToken == "" {
		return invalid("profile_result.lease_token", "is required with generation")
	}
	if r.LeaseToken != "" && len(r.LeaseToken) > 128 {
		return invalid("profile_result.lease_token", "exceeds 128 bytes")
	}
	return nil
}

func validateProfileName(field, value string, required bool) error {
	if err := validString(field, value, required); err != nil {
		return err
	}
	if len(value) > 64 {
		return invalid(field, "exceeds Telegram's 64-byte name bound")
	}
	return nil
}
