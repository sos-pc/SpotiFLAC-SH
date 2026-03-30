package main

import (
	"encoding/json"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend"
	bolt "go.etcd.io/bbolt"
)

var bucketProxies = []byte("api_proxies")

// ProxyConfig est la configuration persistée en BoltDB.
type ProxyConfig struct {
	TidalProxies    []string `json:"tidal_proxies"`
	AmazonProxyBase string   `json:"amazon_proxy_base"`
	DeezerProxyBase string   `json:"deezer_proxy_base"`
}

func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		TidalProxies:    backend.GetTidalProxies(),
		AmazonProxyBase: backend.GetAmazonProxyBase(),
		DeezerProxyBase: backend.GetDeezerProxyBase(),
	}
}

// LoadProxyConfig lit la config depuis BoltDB et applique les setters backend.
// Appelé au démarrage du serveur.
func LoadProxyConfig(db *bolt.DB) {
	var cfg ProxyConfig
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketProxies)
		if b == nil {
			return nil
		}
		v := b.Get([]byte("config"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &cfg)
	})

	if len(cfg.TidalProxies) > 0 {
		backend.SetTidalProxies(cfg.TidalProxies)
	}
	if cfg.AmazonProxyBase != "" {
		backend.SetAmazonProxyBase(cfg.AmazonProxyBase)
	}
	if cfg.DeezerProxyBase != "" {
		backend.SetDeezerProxyBase(cfg.DeezerProxyBase)
	}
}

// GetProxyConfig lit la config courante depuis BoltDB (ou retourne les défauts).
func GetProxyConfig(db *bolt.DB) ProxyConfig {
	var cfg ProxyConfig
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketProxies)
		if b == nil {
			return nil
		}
		v := b.Get([]byte("config"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &cfg)
	})
	if cfg.TidalProxies == nil {
		cfg = defaultProxyConfig()
	}
	return cfg
}

// SaveProxyConfig persiste la config et applique immédiatement les setters.
func SaveProxyConfig(db *bolt.DB, cfg ProxyConfig) error {
	// Nettoyer les entrées vides de la liste Tidal
	clean := make([]string, 0, len(cfg.TidalProxies))
	for _, p := range cfg.TidalProxies {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		clean = defaultProxyConfig().TidalProxies
	}
	cfg.TidalProxies = clean

	if cfg.AmazonProxyBase == "" {
		cfg.AmazonProxyBase = defaultProxyConfig().AmazonProxyBase
	}
	if cfg.DeezerProxyBase == "" {
		cfg.DeezerProxyBase = defaultProxyConfig().DeezerProxyBase
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketProxies)
		if err != nil {
			return err
		}
		return b.Put([]byte("config"), data)
	}); err != nil {
		return err
	}

	// Appliquer immédiatement
	backend.SetTidalProxies(cfg.TidalProxies)
	backend.SetAmazonProxyBase(cfg.AmazonProxyBase)
	backend.SetDeezerProxyBase(cfg.DeezerProxyBase)

	// Invalider le cache de statut pour que le prochain refresh reflète la nouvelle config
	invalidateStatusCache()

	return nil
}
