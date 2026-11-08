package protocol

type PairingResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
}
