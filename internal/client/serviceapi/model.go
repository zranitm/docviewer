package serviceapi

// RecordPage is the Service System API's response, exactly as it sends it.
type RecordPage struct {
	HasMore bool     `json:"has_more"`
	Records []Record `json:"records"`
}

// Record is one Service record on the wire.
type Record struct {
	RecordID     int64  `json:"record_id"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	CreatedAt    int64  `json:"created_at"` // epoch seconds
	DownloadLink string `json:"download_link"`
	WorkNotes    string `json:"work_notes,omitempty"`
	FileSHA256   string `json:"file_sha256,omitempty"`
}
