package commands

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/portainer/portainer/api/filesystem"

	"github.com/rs/zerolog/log"
)

// deploymentFileAction names the two supported post-deployment behaviors.
type deploymentFileAction int

const (
	// iota generates consecutive integer values, similar to a small Java enum.
	deploymentFileArchive deploymentFileAction = iota
	deploymentFileCleanup
)

// cleanDeploymentDir accepts only a safe path relative to the stack directory.
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
		return "", errors.New("deployment directory cannot be current directory")
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("deployment directory %q cannot traverse outside the stack directory", dir)
	}

	return cleaned, nil
}

// deploymentFiles builds the safe, unique list of files whose lifecycle is tied
// to a deployment.
func deploymentFiles(composeRelativeFilePaths []string) []string {
	files := make([]string, 0, len(composeRelativeFilePaths)+3)
	// map[string]struct{} is a common Go set: only the keys matter.
	seen := map[string]struct{}{}

	// A function can be stored in a local variable. This helper validates and
	// de-duplicates a path before adding it to the result slice.
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

	// These files affect a deployment even when they were not explicit CLI args.
	add(".env")
	add("portainer.yml")
	add("portainer.yaml")

	return files
}

// deploymentFileSet converts a slice into a set for quick membership checks.
func deploymentFileSet(files []string) map[string]struct{} {
	set := make(map[string]struct{}, len(files))
	for _, file := range files {
		set[file] = struct{}{}
	}

	return set
}

// restoreDeploymentFiles moves archived files back when the destination file is
// missing. Existing files always win so local changes cannot be overwritten.
func restoreDeploymentFiles(root string, deploymentDir string, files []string) error {
	deploymentDir, err := cleanDeploymentDir(deploymentDir)
	if err != nil {
		return err
	}
	if deploymentDir == "" {
		return nil
	}

	for _, file := range files {
		rootPath := filesystem.JoinPaths(root, file)
		if _, err := os.Lstat(rootPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}

		archivePath := filesystem.JoinPaths(root, deploymentDir, file)
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

// finalizeDeploymentFiles chooses the requested post-deployment action.
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

// finalizeDeploymentFilesAfterDeploy protects deployment files when deployment
// failed. Cleanup or archiving is allowed only after a successful deploy.
func finalizeDeploymentFilesAfterDeploy(root string, deploymentDir string, files []string, cleanup bool, deployErr error) error {
	if deployErr != nil {
		return deployErr
	}

	return finalizeDeploymentFiles(root, deploymentDir, files, cleanup)
}

// cleanupDeploymentFiles deletes only the known deployment files, not the rest of
// the checked-out repository.
func cleanupDeploymentFiles(root string, files []string) error {
	for _, file := range files {
		rootPath := filesystem.JoinPaths(root, file)
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

// archiveDeploymentFiles moves known deployment files below deploymentDir. It
// never overwrites an archive file whose contents are different.
func archiveDeploymentFiles(root string, deploymentDir string, files []string) error {
	deploymentDir, err := cleanDeploymentDir(deploymentDir)
	if err != nil {
		return err
	}
	if deploymentDir == "" {
		return nil
	}

	for _, file := range files {
		rootPath := filesystem.JoinPaths(root, file)
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

		archivePath := filesystem.JoinPaths(root, deploymentDir, file)
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

			// An identical archived copy already exists, so removing the root copy
			// produces the same final state without overwriting anything.
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

// sameFileContent opens two files and compares their bytes.
func sameFileContent(leftPath string, rightPath string) (bool, error) {
	left, err := os.Open(leftPath)
	if err != nil {
		return false, err
	}
	// defer is similar to Java try-with-resources: Close runs on every return path.
	defer func() { _ = left.Close() }()

	right, err := os.Open(rightPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = right.Close() }()

	equal, err := readersEqual(left, right)
	return equal, err
}
