package main

import (
	"encoding/json"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	bolt "go.etcd.io/bbolt"
)

var bucketProxies = []byte("api_proxies")

// ProxyConfig est la configuration persistée en BoltDB.
type ProxyConfig struct {
	TidalProxies []string `json:"tidal_proxies"`
}

// defaultProxyConfig returns the factory defaults — hardcoded values that are
// independent of the current in-memory state (which may have been overridden
// by a saved user configuration).  Used by SaveProxyConfig as a fallback when
// the user submits an empty list, enabling a true "reset to defaults".
func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		TidalProxies: util.GetDefaultTidalProxies(),
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
		util.SetTidalProxies(cfg.TidalProxies)
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

	// Ces URLs deviennent la base de requêtes sortantes faites par le
	// serveur (téléchargements) — un schéma non-http(s) ou une cible privée/
	// loopback ouvrirait une SSRF. Rejeter la config entière plutôt que de
	// dropper silencieusement une entrée invalide (l'admin doit savoir
	// laquelle poser problème).
	for _, list := range [][]string{cfg.TidalProxies} {
		for _, u := range list {
			if err := ValidateExternalURL(u); err != nil {
				return err
			}
		}
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
	util.SetTidalProxies(cfg.TidalProxies)
	// Invalider le cache de statut pour que le prochain refresh reflète la nouvelle config
	invalidateStatusCache()

	return nil
}
