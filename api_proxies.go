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
	TidalProxies   []string `json:"tidal_proxies"`
	QobuzProviders []string `json:"qobuz_providers"`
	AmazonProxies  []string `json:"amazon_proxies"`
	DeezerProxies  []string `json:"deezer_proxies"`
}

func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		TidalProxies:   backend.GetTidalProxies(),
		QobuzProviders: backend.GetQobuzProviders(),
		AmazonProxies:  backend.GetAmazonProxies(),
		DeezerProxies:  backend.GetDeezerProxies(),
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
	if len(cfg.QobuzProviders) > 0 {
		backend.SetQobuzProviders(cfg.QobuzProviders)
	}
	if len(cfg.AmazonProxies) > 0 {
		backend.SetAmazonProxies(cfg.AmazonProxies)
	}
	if len(cfg.DeezerProxies) > 0 {
		backend.SetDeezerProxies(cfg.DeezerProxies)
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
	// Nettoyer les entrées vides des listes
	cleanList := func(in []string, def []string) []string {
		out := make([]string, 0, len(in))
		for _, p := range in {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return def
		}
		return out
	}
	def := defaultProxyConfig()
	cfg.TidalProxies = cleanList(cfg.TidalProxies, def.TidalProxies)
	cfg.QobuzProviders = cleanList(cfg.QobuzProviders, def.QobuzProviders)
	cfg.AmazonProxies = cleanList(cfg.AmazonProxies, def.AmazonProxies)
	cfg.DeezerProxies = cleanList(cfg.DeezerProxies, def.DeezerProxies)

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
	backend.SetQobuzProviders(cfg.QobuzProviders)
	backend.SetAmazonProxies(cfg.AmazonProxies)
	backend.SetDeezerProxies(cfg.DeezerProxies)

	// Invalider le cache de statut pour que le prochain refresh reflète la nouvelle config
	invalidateStatusCache()

	return nil
}
