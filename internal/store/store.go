// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package store holds all SQL. Every query is parameterised (no string-built SQL).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

// Pipeline stages, in order. Each stage must be approved before the next unlocks.
var StageOrder = []string{"storyboard", "voiceover", "audio", "prompts", "images", "videos", "render"}

const (
	StatusLocked     = "locked"
	StatusReady      = "ready"
	StatusGenerating = "generating"
	StatusReview     = "review"
	StatusApproved   = "approved"
)

type Store struct{ DB *pgxpool.Pool }

type Project struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	VideoType         string    `json:"video_type"`
	Brief             string    `json:"brief"`
	TargetDurationSec int       `json:"target_duration_sec"`
	AspectRatio       string    `json:"aspect_ratio"`
	Language          string    `json:"language"`
	Tone              string    `json:"tone"`
	ImageStyle        string    `json:"image_style"`
	CreatedBy         string    `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Stage struct {
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
	Feedback  string          `json:"feedback"`
	Error     string          `json:"error"`
	Version   int             `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Scene struct {
	ID           string  `json:"id"`
	Idx          int     `json:"idx"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	Shot         string  `json:"shot"`
	DurationSec  float64 `json:"duration_sec"`
	Voiceover    string  `json:"voiceover"`
	ImagePrompt  string  `json:"image_prompt"`
	VideoPrompt  string  `json:"video_prompt"`
	AudioAssetID *string `json:"audio_asset_id"`
	ImageAssetID *string `json:"image_asset_id"`
	VideoAssetID *string `json:"video_asset_id"`
}

type Asset struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	SceneID      *string   `json:"scene_id"`
	Kind         string    `json:"kind"`
	StorageName  string    `json:"-"`
	OriginalName string    `json:"original_name"`
	Mime         string    `json:"mime"`
	SizeBytes    int64     `json:"size_bytes"`
	Source       string    `json:"source"`
	Note         string    `json:"note"`
	Consent      bool      `json:"consent"`
	CreatedAt    time.Time `json:"created_at"`
}

// ---------- projects ----------

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO projects(title, video_type, brief, target_duration_sec, aspect_ratio, language, tone, image_style, created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, created_at, updated_at`,
		p.Title, p.VideoType, p.Brief, p.TargetDurationSec, p.AspectRatio, p.Language, p.Tone, p.ImageStyle, p.CreatedBy).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return err
	}
	for i, k := range StageOrder {
		st := StatusLocked
		if i == 0 {
			st = StatusReady
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stages(project_id, kind, status) VALUES($1,$2,$3)`, p.ID, k, st); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const projectCols = `id, title, video_type, brief, target_duration_sec, aspect_ratio, language, tone, image_style, created_by, created_at, updated_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Title, &p.VideoType, &p.Brief, &p.TargetDurationSec, &p.AspectRatio, &p.Language, &p.Tone, &p.ImageStyle, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	return scanProject(s.DB.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id=$1`, id))
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+projectCols+` FROM projects ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	ct, err := s.DB.Exec(ctx, `UPDATE projects SET title=$2, brief=$3, target_duration_sec=$4, aspect_ratio=$5, language=$6, tone=$7, image_style=$8, updated_at=now() WHERE id=$1`,
		p.ID, p.Title, p.Brief, p.TargetDurationSec, p.AspectRatio, p.Language, p.Tone, p.ImageStyle)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteProject removes the project and (via cascade) its stages, scenes and asset rows.
// Returns storage names so the caller can erase the files (data-erasure requirement).
func (s *Store) DeleteProject(ctx context.Context, id string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT storage_name FROM assets WHERE project_id=$1`, id)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	ct, err := s.DB.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return names, err
}

func (s *Store) TouchProject(ctx context.Context, id string) {
	s.DB.Exec(ctx, `UPDATE projects SET updated_at=now() WHERE id=$1`, id)
}

// ---------- stages ----------

func (s *Store) ListStages(ctx context.Context, projectID string) ([]Stage, error) {
	rows, err := s.DB.Query(ctx, `SELECT kind, status, output, feedback, error, version, updated_at FROM stages WHERE project_id=$1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKind := map[string]Stage{}
	for rows.Next() {
		var st Stage
		if err := rows.Scan(&st.Kind, &st.Status, &st.Output, &st.Feedback, &st.Error, &st.Version, &st.UpdatedAt); err != nil {
			return nil, err
		}
		byKind[st.Kind] = st
	}
	out := make([]Stage, 0, len(StageOrder))
	for _, k := range StageOrder {
		if st, ok := byKind[k]; ok {
			out = append(out, st)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetStage(ctx context.Context, projectID, kind string) (*Stage, error) {
	var st Stage
	err := s.DB.QueryRow(ctx, `SELECT kind, status, output, feedback, error, version, updated_at FROM stages WHERE project_id=$1 AND kind=$2`, projectID, kind).
		Scan(&st.Kind, &st.Status, &st.Output, &st.Feedback, &st.Error, &st.Version, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &st, err
}

// ClaimStage atomically moves a stage into "generating" if it is in an allowed state.
func (s *Store) ClaimStage(ctx context.Context, projectID, kind string) (bool, error) {
	ct, err := s.DB.Exec(ctx, `UPDATE stages SET status='generating', error='', updated_at=now()
		WHERE project_id=$1 AND kind=$2 AND status IN ('ready','review')`, projectID, kind)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

func (s *Store) FinishStage(ctx context.Context, projectID, kind string, output any) error {
	b, err := json.Marshal(output)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE stages SET status='review', output=$3, error='', version=version+1, updated_at=now() WHERE project_id=$1 AND kind=$2`, projectID, kind, b)
	return err
}

func (s *Store) FailStage(ctx context.Context, projectID, kind, msg string) error {
	_, err := s.DB.Exec(ctx, `UPDATE stages SET status='ready', error=$3, updated_at=now() WHERE project_id=$1 AND kind=$2`, projectID, kind, msg)
	return err
}

func (s *Store) SetFeedback(ctx context.Context, projectID, kind, feedback string) error {
	_, err := s.DB.Exec(ctx, `UPDATE stages SET feedback=$3, updated_at=now() WHERE project_id=$1 AND kind=$2`, projectID, kind, feedback)
	return err
}

// Approve marks a stage approved and unlocks the next one.
func (s *Store) Approve(ctx context.Context, projectID, kind string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE stages SET status='approved', updated_at=now() WHERE project_id=$1 AND kind=$2 AND status='review'`, projectID, kind)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("stage is not awaiting review")
	}
	for i, k := range StageOrder {
		if k == kind && i+1 < len(StageOrder) {
			if _, err := tx.Exec(ctx, `UPDATE stages SET status='ready', updated_at=now() WHERE project_id=$1 AND kind=$2 AND status='locked'`, projectID, StageOrder[i+1]); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// Reopen puts an approved/review stage back to "review" and re-locks all later stages
// that were not yet generated, so the user can revise an earlier step.
func (s *Store) Reopen(ctx context.Context, projectID, kind string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE stages SET status='review', updated_at=now() WHERE project_id=$1 AND kind=$2 AND status='approved'`, projectID, kind)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("only an approved stage can be reopened")
	}
	after := false
	for _, k := range StageOrder {
		if after {
			// Later stages that already have output go back to review; empty ones lock.
			if _, err := tx.Exec(ctx, `UPDATE stages SET status = CASE WHEN version > 0 THEN 'review' ELSE 'locked' END, updated_at=now()
				WHERE project_id=$1 AND kind=$2 AND status <> 'generating'`, projectID, k); err != nil {
				return err
			}
		}
		if k == kind {
			after = true
		}
	}
	return tx.Commit(ctx)
}

// ResetStuck releases stages left in "generating" by a crash/restart.
func (s *Store) ResetStuck(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE stages SET status='ready', error='Dihentikan karena server restart' WHERE status='generating'`)
	return err
}

// ---------- scenes ----------

const sceneCols = `id, idx, title, description, shot, duration_sec::float8, voiceover, image_prompt, video_prompt, audio_asset_id, image_asset_id, video_asset_id`

func scanScene(row pgx.Row) (*Scene, error) {
	var sc Scene
	err := row.Scan(&sc.ID, &sc.Idx, &sc.Title, &sc.Description, &sc.Shot, &sc.DurationSec, &sc.Voiceover, &sc.ImagePrompt, &sc.VideoPrompt, &sc.AudioAssetID, &sc.ImageAssetID, &sc.VideoAssetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sc, err
}

func (s *Store) ListScenes(ctx context.Context, projectID string) ([]Scene, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+sceneCols+` FROM scenes WHERE project_id=$1 ORDER BY idx`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scene{}
	for rows.Next() {
		sc, err := scanScene(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

func (s *Store) GetScene(ctx context.Context, projectID, sceneID string) (*Scene, error) {
	return scanScene(s.DB.QueryRow(ctx, `SELECT `+sceneCols+` FROM scenes WHERE project_id=$1 AND id=$2`, projectID, sceneID))
}

// ReplaceScenes swaps the whole scene list (used when a storyboard is (re)generated).
// Scene-scoped uploaded references are kept only if they belong to a scene that survives,
// so they are deleted along with the old scenes; generated media is dropped.
func (s *Store) ReplaceScenes(ctx context.Context, projectID string, scenes []Scene) ([]string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT storage_name FROM assets WHERE project_id=$1 AND scene_id IS NOT NULL`, projectID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scenes WHERE project_id=$1`, projectID); err != nil {
		return nil, err
	}
	for i, sc := range scenes {
		if _, err := tx.Exec(ctx, `INSERT INTO scenes(project_id, idx, title, description, shot, duration_sec) VALUES($1,$2,$3,$4,$5,$6)`,
			projectID, i+1, sc.Title, sc.Description, sc.Shot, sc.DurationSec); err != nil {
			return nil, err
		}
	}
	return names, tx.Commit(ctx)
}

// UpdateSceneText updates editable text fields. Empty map keys are ignored.
func (s *Store) UpdateSceneText(ctx context.Context, projectID, sceneID string, f map[string]any) error {
	allowed := map[string]string{
		"title": "title", "description": "description", "shot": "shot", "duration_sec": "duration_sec",
		"voiceover": "voiceover", "image_prompt": "image_prompt", "video_prompt": "video_prompt",
	}
	for k, v := range f {
		col, ok := allowed[k]
		if !ok {
			continue
		}
		// col comes from the fixed allow-list above, never from user input.
		if _, err := s.DB.Exec(ctx, `UPDATE scenes SET `+col+`=$3, updated_at=now() WHERE project_id=$1 AND id=$2`, projectID, sceneID, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetSceneAsset(ctx context.Context, sceneID, field, assetID string) error {
	cols := map[string]string{"audio": "audio_asset_id", "image": "image_asset_id", "video": "video_asset_id"}
	col, ok := cols[field]
	if !ok {
		return errors.New("bad field")
	}
	_, err := s.DB.Exec(ctx, `UPDATE scenes SET `+col+`=$2, updated_at=now() WHERE id=$1`, sceneID, assetID)
	return err
}

// ---------- assets ----------

const assetCols = `id, project_id, scene_id, kind, storage_name, original_name, mime, size_bytes, source, note, consent, created_at`

func scanAsset(row pgx.Row) (*Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.ProjectID, &a.SceneID, &a.Kind, &a.StorageName, &a.OriginalName, &a.Mime, &a.SizeBytes, &a.Source, &a.Note, &a.Consent, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) CreateAsset(ctx context.Context, a *Asset) error {
	return s.DB.QueryRow(ctx, `INSERT INTO assets(project_id, scene_id, kind, storage_name, original_name, mime, size_bytes, source, note, consent)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id, created_at`,
		a.ProjectID, a.SceneID, a.Kind, a.StorageName, a.OriginalName, a.Mime, a.SizeBytes, a.Source, a.Note, a.Consent).Scan(&a.ID, &a.CreatedAt)
}

func (s *Store) GetAsset(ctx context.Context, id string) (*Asset, error) {
	return scanAsset(s.DB.QueryRow(ctx, `SELECT `+assetCols+` FROM assets WHERE id=$1`, id))
}

func (s *Store) ListAssets(ctx context.Context, projectID string) ([]Asset, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+assetCols+` FROM assets WHERE project_id=$1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// References returns uploaded reference assets for the project (sceneID=="" → project-level only)
// plus, when sceneID is given, that scene's own references.
func (s *Store) References(ctx context.Context, projectID, sceneID string) ([]Asset, error) {
	all, err := s.ListAssets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := []Asset{}
	for _, a := range all {
		if a.Source != "upload" {
			continue
		}
		if a.SceneID == nil || (sceneID != "" && *a.SceneID == sceneID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Store) DeleteAsset(ctx context.Context, projectID, id string) (string, error) {
	var name string
	err := s.DB.QueryRow(ctx, `DELETE FROM assets WHERE project_id=$1 AND id=$2 RETURNING storage_name`, projectID, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ---------- audit ----------

func (s *Store) Audit(ctx context.Context, actor, remote, action, entity, entityID string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	b, _ := json.Marshal(detail)
	s.DB.Exec(ctx, `INSERT INTO audit_log(actor, remote_addr, action, entity, entity_id, detail) VALUES($1,$2,$3,$4,$5,$6)`,
		actor, remote, action, entity, entityID, b)
}
