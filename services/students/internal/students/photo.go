package students

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxStudentPhotoBytes = 1 << 20 // 1 MB

// SaveStudentPhoto stores one image file for the student and keeps only the URL.
// A new photo replaces the previous file.
func (s *Service) SaveStudentPhoto(ctx context.Context, facilityID, studentID int64, payload, ext string) (string, error) {
	if facilityID <= 0 || studentID <= 0 {
		return "", fmt.Errorf("invalid_request")
	}
	if err := s.studentInFacility(ctx, facilityID, studentID); err != nil {
		return "", err
	}
	raw, kind, err := decodeStudentPhoto(payload, ext)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(s.Cfg.PhotoDir)
	if dir == "" {
		dir = "/data/student-photos"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, old := range []string{"jpg", "png", "webp"} {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.%s", studentID, old)))
	}
	name := fmt.Sprintf("%d.%s", studentID, kind)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		return "", err
	}
	url := fmt.Sprintf("/v1/students/photos/%s?v=%d", name, time.Now().Unix())
	if _, err := s.DB.ExecContext(ctx, `
UPDATE students SET photo_url=$2, updated_at=NOW() WHERE id=$1`, studentID, url); err != nil {
		return "", err
	}
	return url, nil
}

// ClearStudentPhoto removes the file and the URL. The name initial is shown again.
func (s *Service) ClearStudentPhoto(ctx context.Context, facilityID, studentID int64) error {
	if facilityID <= 0 || studentID <= 0 {
		return fmt.Errorf("invalid_request")
	}
	if err := s.studentInFacility(ctx, facilityID, studentID); err != nil {
		return err
	}
	s.removeStudentPhotoFiles(studentID)
	_, err := s.DB.ExecContext(ctx, `
UPDATE students SET photo_url='', updated_at=NOW() WHERE id=$1`, studentID)
	return err
}

// StudentPhotoFile returns the on-disk image when the caller may see this student.
func (s *Service) StudentPhotoFile(ctx context.Context, studentID int64, allow func(facilityID int64) bool) (path, contentType string, err error) {
	if studentID <= 0 {
		return "", "", fmt.Errorf("not_found")
	}
	var url string
	err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(photo_url,'') FROM students WHERE id=$1`, studentID).Scan(&url)
	if err == sql.ErrNoRows || strings.TrimSpace(url) == "" {
		return "", "", fmt.Errorf("not_found")
	}
	if err != nil {
		return "", "", err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT facility_id FROM student_enrollments WHERE student_id=$1`, studentID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	ok := false
	for rows.Next() {
		var fid int64
		if err := rows.Scan(&fid); err != nil {
			return "", "", err
		}
		if allow == nil || allow(fid) {
			ok = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("forbidden_facility")
	}
	kind := photoExt(url)
	dir := strings.TrimSpace(s.Cfg.PhotoDir)
	if dir == "" {
		dir = "/data/student-photos"
	}
	path = filepath.Join(dir, fmt.Sprintf("%d.%s", studentID, kind))
	if _, err := os.Stat(path); err != nil {
		return "", "", fmt.Errorf("not_found")
	}
	switch kind {
	case "png":
		contentType = "image/png"
	case "webp":
		contentType = "image/webp"
	default:
		contentType = "image/jpeg"
	}
	return path, contentType, nil
}

func (s *Service) studentInFacility(ctx context.Context, facilityID, studentID int64) error {
	var one int
	err := s.DB.QueryRowContext(ctx, `
SELECT 1 FROM student_enrollments WHERE facility_id=$1 AND student_id=$2 LIMIT 1`,
		facilityID, studentID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("student_not_found")
	}
	return err
}

func (s *Service) removeStudentPhotoFiles(studentID int64) {
	dir := strings.TrimSpace(s.Cfg.PhotoDir)
	if dir == "" {
		return
	}
	for _, ext := range []string{"jpg", "png", "webp"} {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.%s", studentID, ext)))
	}
}

func decodeStudentPhoto(payload, ext string) ([]byte, string, error) {
	payload = strings.TrimSpace(payload)
	if i := strings.Index(payload, ","); i >= 0 && strings.HasPrefix(strings.ToLower(payload), "data:") {
		payload = payload[i+1:]
	}
	if payload == "" {
		return nil, "", fmt.Errorf("image_required")
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil, "", fmt.Errorf("invalid_image")
	}
	if len(raw) == 0 || len(raw) > maxStudentPhotoBytes {
		return nil, "", fmt.Errorf("image_too_large")
	}
	kind := sniffImageExt(raw)
	if kind == "" {
		kind = photoExt(ext)
	}
	if kind != "jpg" && kind != "png" && kind != "webp" {
		return nil, "", fmt.Errorf("unsupported_image")
	}
	if sniffed := sniffImageExt(raw); sniffed != "" && sniffed != kind {
		kind = sniffed
	}
	if sniffImageExt(raw) == "" {
		return nil, "", fmt.Errorf("unsupported_image")
	}
	return raw, kind, nil
}

func sniffImageExt(raw []byte) string {
	if len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF {
		return "jpg"
	}
	if len(raw) >= 8 && raw[0] == 0x89 && raw[1] == 0x50 && raw[2] == 0x4E && raw[3] == 0x47 {
		return "png"
	}
	if len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP" {
		return "webp"
	}
	return ""
}

func photoExt(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimPrefix(raw, ".")
	if i := strings.LastIndex(raw, "."); i >= 0 {
		raw = raw[i+1:]
	}
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[:i]
	}
	switch raw {
	case "jpeg", "jpg":
		return "jpg"
	case "png":
		return "png"
	case "webp":
		return "webp"
	default:
		return ""
	}
}
