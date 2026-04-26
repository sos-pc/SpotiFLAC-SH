package util

import (
	"io"
	"time"
)

// ProgressWriter mesure la vitesse/progression d'un téléchargement.
// Tous les 256 Ko, si SpeedCallback est défini, il est appelé avec
// (mbDownloaded float64, speedMBps float64).
type ProgressWriter struct {
	writer        io.Writer
	total         int64
	lastPrinted   int64
	lastTime      int64
	lastBytes     int64
	SpeedCallback func(mbDownloaded, speedMBps float64)
}

// NewProgressWriter crée un ProgressWriter sans callback (progression silencieuse).
func NewProgressWriter(writer io.Writer) *ProgressWriter {
	now := getCurrentTimeMillis()
	return &ProgressWriter{
		writer:   writer,
		lastTime: now,
	}
}

// NewProgressWriterWithCallback crée un ProgressWriter qui appelle cb tous les 256 Ko.
func NewProgressWriterWithCallback(writer io.Writer, cb func(mbDownloaded, speedMBps float64)) *ProgressWriter {
	pw := NewProgressWriter(writer)
	pw.SpeedCallback = cb
	return pw
}

func getCurrentTimeMillis() int64 {
	return time.Now().UnixMilli()
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	pw.total += int64(n)

	if pw.total-pw.lastPrinted >= 256*1024 {
		mbDownloaded := float64(pw.total) / (1024 * 1024)

		now := getCurrentTimeMillis()
		timeDiff := float64(now-pw.lastTime) / 1000.0
		bytesDiff := float64(pw.total - pw.lastBytes)

		var speedMBps float64
		if timeDiff > 0 {
			speedMBps = (bytesDiff / (1024 * 1024)) / timeDiff
		}

		if pw.SpeedCallback != nil {
			pw.SpeedCallback(mbDownloaded, speedMBps)
		}

		pw.lastPrinted = pw.total
		pw.lastTime = now
		pw.lastBytes = pw.total
	}

	return n, err
}

// GetTotal retourne le nombre d'octets écrits.
func (pw *ProgressWriter) GetTotal() int64 {
	return pw.total
}
