// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import "net/http"

// dshFileUploadPath is dsh's raw-byte upload route (@deepseek-ai/dsh-client-
// file-upload, FILE_UPLOAD_PATH): a file dropped in the composer is posted
// here as its own bytes.
const dshFileUploadPath = "/api/session/uploadFileBinary"

// restoreUploadContentType gives a file upload back the media type it was
// sent with. The sealed relay cannot know the type of what it unseals (the
// browser's Content-Type is the sealed envelope's), so it hands every body on
// as application/json. dsh's upload route takes raw bytes and refuses any
// other type with 415, so no dropped file ever arrived ("Upload failed").
// The route only ever carries raw bytes, so its type is not in doubt.
func restoreUploadContentType(req *http.Request) {
	if req.Method == http.MethodPost && req.URL.Path == dshFileUploadPath {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
}
