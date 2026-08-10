package domain

type KeyProvider interface {
	ActiveKey() (keyID string, secret []byte)
	GetKey(keyID string) ([]byte, bool)
	AllKeys() map[string][]byte
}

type StaticKeyProvider struct {
	activeKeyID  string
	activeSecret []byte
	legacyKeys   map[string][]byte
}

func NewStaticKeyProvider(activeKeyID string, activeSecret string, legacyKeys map[string]string) *StaticKeyProvider {
	if activeKeyID == "" {
		activeKeyID = "v1"
	}
	legacy := make(map[string][]byte, len(legacyKeys))
	for k, v := range legacyKeys {
		if k != "" && v != "" {
			legacy[k] = []byte(v)
		}
	}
	return &StaticKeyProvider{
		activeKeyID:  activeKeyID,
		activeSecret: []byte(activeSecret),
		legacyKeys:   legacy,
	}
}

func (p *StaticKeyProvider) ActiveKey() (string, []byte) {
	if len(p.activeSecret) == 0 {
		return "", nil
	}
	return p.activeKeyID, p.activeSecret
}

func (p *StaticKeyProvider) GetKey(keyID string) ([]byte, bool) {
	if len(p.activeSecret) == 0 && len(p.legacyKeys) == 0 {
		return nil, false
	}
	if keyID == "" || keyID == p.activeKeyID {
		if len(p.activeSecret) > 0 {
			return p.activeSecret, true
		}
	}
	secret, ok := p.legacyKeys[keyID]
	if ok && len(secret) > 0 {
		return secret, true
	}
	return nil, false
}

func (p *StaticKeyProvider) AllKeys() map[string][]byte {
	all := make(map[string][]byte, len(p.legacyKeys)+1)
	if len(p.activeSecret) > 0 {
		all[p.activeKeyID] = p.activeSecret
	}
	for k, v := range p.legacyKeys {
		all[k] = v
	}
	return all
}
