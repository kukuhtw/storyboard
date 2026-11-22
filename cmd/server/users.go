// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"storyboard/internal/auth"
	"storyboard/internal/config"
	"storyboard/internal/store"
)

// bootstrapAdmin creates the first admin from ADMIN_USERNAME / ADMIN_PASSWORD when
// the users table is empty. The password must be changed at first login.
func bootstrapAdmin(ctx context.Context, st *store.Store, cfg *config.Config) error {
	if cfg.LegacyAccessToken {
		slog.Warn("APP_ACCESS_TOKEN is no longer used (login is per user now); remove it from the environment")
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		if cfg.AdminPassword != "" {
			slog.Warn("ADMIN_PASSWORD is only used to create the first admin; remove it from the environment")
		}
		return nil
	}
	if cfg.AdminPassword == "" {
		slog.Warn("no users exist yet: set ADMIN_PASSWORD (and optionally ADMIN_USERNAME) and restart, " +
			"or run `storyboard reset-password <username>` to create the first admin")
		return nil
	}
	username, err := auth.NormalizeUsername(cfg.AdminUsername)
	if err != nil {
		return fmt.Errorf("ADMIN_USERNAME: %w", err)
	}
	if err := auth.ValidatePassword(cfg.AdminPassword, username); err != nil {
		return fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	u := &store.User{Username: username, DisplayName: "Administrator", Role: store.RoleAdmin,
		PasswordHash: hash, MustChangePassword: true, CreatedBy: "bootstrap"}
	if err := st.CreateUser(ctx, u); err != nil {
		return err
	}
	st.Audit(ctx, "bootstrap", "", "user_create", "user", u.ID, map[string]any{"username": username, "role": store.RoleAdmin})
	slog.Info("first admin created; change the password at first login and remove ADMIN_PASSWORD from the environment", "username", username)
	return nil
}

// resetPassword is the break-glass command (e.g. from the Dokploy terminal):
//
//	storyboard reset-password <username>
//
// It creates the user as admin if missing, otherwise resets the password, unlocks and
// re-activates the account. The temporary password is printed once to stdout.
func resetPassword(ctx context.Context, st *store.Store, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: storyboard reset-password <username>")
		return 2
	}
	username, err := auth.NormalizeUsername(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	pw := auth.TempPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	u, err := st.GetUserByUsername(ctx, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		u = &store.User{Username: username, DisplayName: "Administrator", Role: store.RoleAdmin,
			PasswordHash: hash, MustChangePassword: true, CreatedBy: "cli"}
		if err := st.CreateUser(ctx, u); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		st.Audit(ctx, "cli", "", "user_create", "user", u.ID, map[string]any{"username": username, "role": store.RoleAdmin})
		fmt.Printf("Admin %q dibuat.\n", username)
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	default:
		if err := st.SetPassword(ctx, u.ID, hash, true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !u.Active {
			if err := st.UpdateUser(ctx, u.ID, u.DisplayName, u.Role, true); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		st.Audit(ctx, "cli", "", "user_password_reset", "user", u.ID, map[string]any{"username": username, "reactivated": !u.Active})
		fmt.Printf("Password %q direset, akun dibuka & diaktifkan, semua sesi dicabut.\n", username)
	}
	fmt.Printf("Password sementara (wajib diganti saat login): %s\n", pw)
	return 0
}
