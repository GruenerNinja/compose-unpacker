package commands

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

type deploymentFileAction int

const (
	deploymentFileArchive deploymentFileAction = iota
	deploymentFileCleanup
)

func cleanDeploymentDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}

	if path.IsAbs(dir) || filepath.VolumeName(dir) != "" || strings.Contains(dir, `\`) {
		return "", fmt.Errorf("deployment directory %q must be a relative path", dir)
	}

	cleaned := path.Clean(dir)
	if cleaned == "." {
		return "", fmt.Errorf("deployment directory must not be .")
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("deployment directory %q cannot traverse outside the stack directory", dir)
	}

	return cleaned, nil
}

func deploymentFiles(composeRelativeFilePaths []string) []string {
	files := make([]string, 0, len(composeRelativeFilePaths)+3)
	seen := map[string]struct{}{}

	add := func(file string) {
		file = strings.TrimLeft(strings.TrimSpace(file), "/")
		if file == "" {
			return
		}

		file = path.Clean(file)
		if file == "." || file == ".." || strings.HasPrefix(file, "../") || strings.Contains(file, `\`) {
			return
		}

		if _, ok := seen[file]; ok {
			return
		}

		seen[file] = struct{}{}
		files = append(files, file)
	}

	for _, file := range composeRelativeFilePaths {
		add(file)
	}

	add(".env")
	add("portainer.yml")
	add("portainer.yaml")

	return files
}

func deploymentFileSet(files []string) map[string]struct{} {
	set := make(map[string]struct{}, len(files))
	for _, file := range files {
		set[file] = struct{}{}
	}

	return set
}

func restoreDeploymentFiles(root string, deploymentDir string, files []string) error {
	deploymentDir, err := cleanDeploymentDir(deploymentDir)
	if err != nil {
		return err
	}
	if deploymentDir == "" {
		return nil
	}

	for _, file := range files {
		rootPath := filepath.Join(root, filepath.FromSlash(file))
		if _, err := os.Lstat(rootPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}

		archivePath := filepath.Join(root, filepath.FromSlash(deploymentDir), filepath.FromSlash(file))
		info, err := os.Lstat(archivePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return err
		}

		if info.IsDir() {
			log.Warn().
				Str("path", archivePath).
				Msg("Skipping deployment file restore because archived path is a directory")
			continue
		}

		if err := os.MkdirAll(filepath.Dir(rootPath), 0755); err != nil {
			return err
		}

		if err := os.Rename(archivePath, rootPath); err != nil {
			return err
		}
	}

	return nil
}

func finalizeDeploymentFiles(root string, deploymentDir string, files []string, cleanup bool) error {
	action := deploymentFileArchive
	if cleanup {
		action = deploymentFileCleanup
	}

	switch action {
	case deploymentFileCleanup:
		return cleanupDeploymentFiles(root, files)
	case deploymentFileArchive:
		return archiveDeploymentFiles(root, deploymentDir, files)
	default:
		return nil
	}
}

func finalizeDeploymentFilesAfterDeploy(root string, deploymentDir string, files []string, cleanup bool, deployErr error) error {
	if deployErr != nil {
		return deployErr
	}

	return finalizeDeploymentFiles(root, deploymentDir, files, cleanup)
}

func cleanupDeploymentFiles(root string, files []string) error {
	for _, file := range files {
		rootPath := filepath.Join(root, filepath.FromSlash(file))
		info, err := os.Lstat(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return err
		}

		if info.IsDir() {
			log.Warn().
				Str("path", rootPath).
				Msg("Skipping deployment file cleanup because path is a directory")
			continue
		}

		if err := os.Remove(rootPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

func archiveDeploymentFiles(root string, deploymentDir string, files []string) error {
	deploymentDir, err := cleanDeploymentDir(deploymentDir)
	if err != nil {
		return err
	}
	if deploymentDir == "" {
		return nil
	}

	for _, file := range files {
		rootPath := filepath.Join(root, filepath.FromSlash(file))
		info, err := os.Lstat(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return err
		}

		if info.IsDir() {
			log.Warn().
				Str("path", rootPath).
				Msg("Skipping deployment file archive because path is a directory")
			continue
		}

		archivePath := filepath.Join(root, filepath.FromSlash(deploymentDir), filepath.FromSlash(file))
		archiveInfo, err := os.Lstat(archivePath)
		if err == nil {
			if archiveInfo.IsDir() {
				log.Warn().
					Str("source", rootPath).
					Str("target", archivePath).
					Msg("Skipping deployment file archive because target is a directory")
				continue
			}

			identical, err := sameFileContent(rootPath, archivePath)
			if err != nil {
				return err
			}

			if !identical {
				log.Warn().
					Str("source", rootPath).
					Str("target", archivePath).
					Msg("Skipping deployment file archive because target has local changes")
				continue
			}

			if err := os.Remove(rootPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
			return err
		}

		if err := os.Rename(rootPath, archivePath); err != nil {
			return err
		}
	}

	return nil
}

func sameFileContent(leftPath string, rightPath string) (bool, error) {
	left, err := os.Open(leftPath)
	if err != nil {
		return false, err
	}
	defer left.Close()

	right, err := os.Open(rightPath)
	if err != nil {
		return false, err
	}
	defer right.Close()

	equal, err := readersEqual(left, right)
	return equal, err
}
