package attribution

import (
	"strings"
)

// Label keys cloud0 suggests (the built-in label rules honour exactly these).
const (
	suggestProductKey = ProductLabelKey
	suggestEnvKey     = "env"
)

// parsedGlobal is the structured form of a resource.global_name such as
// //compute.googleapis.com/projects/P/zones/Z/instances/N.
type parsedGlobal struct {
	Host     string
	Project  string
	Zone     string
	Region   string // from /regions/ or /locations/
	Global   bool
	Kind     string // last collection segment: instances, disks, ...
	Name     string
	Resource string // resource type constant (TypeXxx)
}

// parseGlobalName parses a resource.global_name; unrecognised shapes yield
// Resource == TypeUnknown (other fields are filled as far as possible).
func parseGlobalName(g string) parsedGlobal {
	p := parsedGlobal{Resource: TypeUnknown}
	g = strings.TrimSpace(g)
	if !strings.HasPrefix(g, "//") {
		return p
	}
	parts := strings.Split(strings.TrimPrefix(g, "//"), "/")
	if len(parts) < 3 {
		return p
	}
	p.Host = strings.ToLower(parts[0])
	rest := parts[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "global" {
			p.Global = true
			continue
		}
		if i+1 >= len(rest) {
			break
		}
		k, v := rest[i], rest[i+1]
		i++
		switch k {
		case "projects":
			p.Project = v
		case "zones":
			p.Zone = v
		case "regions", "locations":
			p.Region = v
		default:
			p.Kind, p.Name = k, v
		}
	}
	switch {
	case strings.HasPrefix(p.Host, "compute.") && p.Kind == "instances":
		p.Resource = TypeComputeInstance
	case strings.HasPrefix(p.Host, "compute.") && p.Kind == "disks":
		p.Resource = TypeComputeDisk
	case strings.HasPrefix(p.Host, "compute.") && p.Kind == "addresses":
		p.Resource = TypeComputeAddress
	case strings.HasPrefix(p.Host, "run.") && p.Kind == "services":
		p.Resource = TypeCloudRunService
	case strings.HasPrefix(p.Host, "run.") && p.Kind == "jobs":
		p.Resource = TypeCloudRunJob
	case strings.HasPrefix(p.Host, "storage.") && p.Kind == "buckets":
		p.Resource = TypeGCSBucket
	case strings.HasPrefix(p.Host, "sqladmin.") && p.Kind == "instances":
		p.Resource = TypeCloudSQLInstance
	case strings.HasPrefix(p.Host, "artifactregistry.") && p.Kind == "repositories":
		p.Resource = TypeArtifactRepository
	}
	if p.Resource == TypeUnknown {
		p.Name = ""
	}
	return p
}

// typeFromService is the fallback when the global name does not identify the
// resource type: it is derived from the service and SKU descriptions.
func typeFromService(service, sku string) string {
	svc := strings.ToLower(service)
	s := strings.ToLower(sku)
	switch {
	case strings.Contains(svc, "compute engine"):
		switch {
		case strings.Contains(s, "static ip") || strings.Contains(s, "reserved ip") || strings.Contains(s, "ip address"):
			return TypeComputeAddress
		case strings.Contains(s, "pd capacity") || strings.Contains(s, "storage pd") ||
			strings.Contains(s, "persistent disk") || strings.Contains(s, "hyperdisk") || strings.Contains(s, "disk"):
			return TypeComputeDisk
		case strings.Contains(s, "snapshot") || strings.Contains(s, "image") || strings.Contains(s, "network") || strings.Contains(s, "egress"):
			return TypeUnknown
		default:
			return TypeComputeInstance
		}
	case strings.Contains(svc, "cloud run"):
		if strings.Contains(s, "job") {
			return TypeCloudRunJob
		}
		return TypeCloudRunService
	case strings.Contains(svc, "cloud storage"):
		return TypeGCSBucket
	case strings.Contains(svc, "cloud sql"):
		return TypeCloudSQLInstance
	case strings.Contains(svc, "artifact registry"):
		return TypeArtifactRepository
	}
	return TypeUnknown
}

// shellSafe reports whether s needs no quoting.
func shellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// shellQuote returns s unchanged when it only has [A-Za-z0-9._-], else a
// single-quoted form with embedded quotes escaped.
func shellQuote(s string) string {
	if shellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sanitizeLabelValue lowercases v and maps every character outside
// [a-z0-9_-] to '-', truncating to 63 characters. "" means unusable.
func sanitizeLabelValue(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(v)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 63 {
			break
		}
	}
	return b.String()
}

// labelArg renders labels as product=<p>,env=<e> (product first).
func labelArg(labels map[string]string) string {
	var out []string
	for _, k := range []string{suggestProductKey, suggestEnvKey} {
		if v, ok := labels[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return strings.Join(out, ",")
}

// riskText returns the conservative risk wording per resource type.
func riskText(rtype string) string {
	const billing = " Label values are visible in billing exports."
	switch rtype {
	case TypeComputeInstance:
		return "Metadata-only change; no restart." + billing
	case TypeComputeDisk:
		return "Metadata-only change; the disk stays attached and no restart is needed." + billing
	case TypeComputeAddress:
		return "Metadata-only change; the address stays reserved and attached." + billing
	case TypeCloudRunService:
		return "Creates a new Cloud Run revision (traffic follows the latest revision if so configured); do it in a quiet window."
	case TypeCloudRunJob:
		return "Updates the Cloud Run job definition (a new job generation); running executions are not interrupted, but verify before applying."
	case TypeGCSBucket:
		return "Metadata-only change; objects are untouched." + billing
	case TypeCloudSQLInstance:
		return "Runs a patch operation; confirm no unrelated pending settings changes."
	case TypeArtifactRepository:
		return "Metadata-only change; stored images and packages are untouched." + billing
	}
	return "Resource type not recognised; add these labels to the resource in the Cloud console." + billing
}

// locationNote is appended to risk when a needed zone/region is unknown.
func locationNote(rtype string) string {
	switch rtype {
	case TypeComputeInstance:
		return " Zone could not be determined from the billing export, so no command is given; look it up with gcloud compute instances list."
	case TypeComputeDisk:
		return " Zone or region could not be determined from the billing export, so no command is given; look it up with gcloud compute disks list."
	case TypeComputeAddress:
		return " Region could not be determined (global or unknown address), so no command is given; look it up with gcloud compute addresses list."
	case TypeCloudRunService, TypeCloudRunJob, TypeArtifactRepository:
		return " Region could not be determined from the billing export, so no command is given; look it up in the Cloud console."
	}
	return ""
}

// buildCommand returns the gcloud command for rtype, or nil when the type is
// unknown or a required project/name/location is missing or unsafe. Every
// argument is shell-quoted when it contains characters outside [A-Za-z0-9._-].
func buildCommand(rtype, project, name, zone, region string, labels map[string]string) *string {
	if project == "" || name == "" || strings.HasPrefix(name, "-") || labelArg(labels) == "" {
		return nil
	}
	for _, v := range []string{project, zone, region} {
		if strings.HasPrefix(v, "-") {
			return nil
		}
	}
	la := labelArg(labels)
	n, p := shellQuote(name), "--project="+shellQuote(project)
	var cmd string
	switch rtype {
	case TypeComputeInstance:
		if zone == "" {
			return nil
		}
		cmd = "gcloud compute instances add-labels " + n + " " + p + " --zone=" + shellQuote(zone) + " --labels=" + la
	case TypeComputeDisk:
		switch {
		case zone != "":
			cmd = "gcloud compute disks add-labels " + n + " " + p + " --zone=" + shellQuote(zone) + " --labels=" + la
		case region != "":
			cmd = "gcloud compute disks add-labels " + n + " " + p + " --region=" + shellQuote(region) + " --labels=" + la
		default:
			return nil
		}
	case TypeComputeAddress:
		if region == "" {
			return nil
		}
		cmd = "gcloud compute addresses add-labels " + n + " " + p + " --region=" + shellQuote(region) + " --labels=" + la
	case TypeCloudRunService:
		if region == "" {
			return nil
		}
		cmd = "gcloud run services update " + n + " " + p + " --region=" + shellQuote(region) + " --update-labels=" + la
	case TypeCloudRunJob:
		if region == "" {
			return nil
		}
		cmd = "gcloud run jobs update " + n + " " + p + " --region=" + shellQuote(region) + " --update-labels=" + la
	case TypeGCSBucket:
		bucket := "gs://" + name
		if !shellSafe(name) {
			bucket = "'" + strings.ReplaceAll(bucket, "'", `'\''`) + "'"
		}
		cmd = "gcloud storage buckets update " + bucket + " " + p + " --update-labels=" + la
	case TypeCloudSQLInstance:
		cmd = "gcloud sql instances patch " + n + " " + p + " --update-labels=" + la
	case TypeArtifactRepository:
		if region == "" {
			return nil
		}
		cmd = "gcloud artifacts repositories update " + n + " " + p + " --location=" + shellQuote(region) + " --update-labels=" + la
	default:
		return nil
	}
	return &cmd
}
