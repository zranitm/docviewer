package salesapi

// DocumentPage is the Sales System API's response, exactly as it sends it.
type DocumentPage struct {
	VehicleVIN string     `json:"vehicleVin"`
	HasMore    bool       `json:"hasMore"`
	Documents  []Document `json:"documents"`
}

// Document is one Sales document on the wire.
type Document struct {
	DocID       string `json:"docId"`
	DocType     string `json:"docType"`
	Title       string `json:"title"`
	CreatedDate string `json:"createdDate"` // YYYY-MM-DD
	FileURL     string `json:"fileUrl"`
	Notes       string `json:"notes,omitempty"`
	FileSHA256  string `json:"fileSha256,omitempty"`
}
