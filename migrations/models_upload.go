package migrations

import (
	domainupload "crm-core/domain/upload"
)

// uploadModels lists the uploaded image metadata table.
func uploadModels() []any {
	return []any{&domainupload.UploadedFile{}}
}
