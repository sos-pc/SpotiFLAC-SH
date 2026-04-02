package tidal

// GetPublicTidalToken retourne un token statique public pour l'API Tidal.
// Ce token (x-tidal-token) permet d'effectuer des requêtes de recherche
// et de résolution d'ISRC sans avoir besoin d'une authentification complète (Device Flow).
func GetPublicTidalToken() string {
	return "CzET4vdadNUFQ5JU"
}
