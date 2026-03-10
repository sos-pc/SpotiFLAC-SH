package backend

// GetSeparator retourne le séparateur utilisé pour joindre artistes, genres, etc.
// Défini ici car backend/spotfetch.go n'existe pas dans ce fork.
func GetSeparator() string {
	return ", "
}
