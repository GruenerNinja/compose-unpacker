package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/portainer/compose-unpacker/auth"
	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/api/filesystem"
	portainergit "github.com/portainer/portainer/api/git"
	"github.com/portainer/portainer/pkg/fips"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogitfs "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/rs/zerolog/log"
)

// gitRepositoryOptions groups the values needed by Git helper functions. Unlike
// command structs, its fields are private and are not read directly by Kong.
type gitRepositoryOptions struct {
	repository            string
	reference             string
	user                  string
	password              string
	skipTLSVerify         bool
	keep                  bool
	mountPath             string
	clonePath             string
	sourceDir             string
	unprotectMissingPaths map[string]struct{}
}

// disallowedFlatDestinations protects important system trees from flat mode,
// which may delete and replace files in its destination.
var disallowedFlatDestinations = []string{
	string(filepath.Separator),
	string(filepath.Separator) + "bin",
	string(filepath.Separator) + "dev",
	string(filepath.Separator) + "etc",
	string(filepath.Separator) + "proc",
	string(filepath.Separator) + "run",
	string(filepath.Separator) + "root",
	string(filepath.Separator) + "sbin",
	string(filepath.Separator) + "sys",
	string(filepath.Separator) + "usr",
	string(filepath.Separator) + "var" + string(filepath.Separator) + "run",
}

// gitRepositoryDeploymentPaths returns both the removable stack directory and the
// exact directory that contains the checked-out repository files.
func gitRepositoryDeploymentPaths(destination string, projectName string, repositoryName string, flat bool) (string, string) {
	// Normal mode has a stack directory containing one cloned repository.
	mountPath := exec.MakeWorkingDir(destination, projectName)
	clonePath := filesystem.JoinPaths(mountPath, repositoryName)
	// Flat mode uses the destination itself for both concepts.
	if flat {
		mountPath = destination
		clonePath = destination
	}

	return mountPath, clonePath
}

// gitRepositoryMountPath returns the directory that may be removed after an
// undeploy. It is wider than clonePath in normal mode.
func gitRepositoryMountPath(destination string, projectName string, flat bool) string {
	if flat {
		return destination
	}

	return exec.MakeWorkingDir(destination, projectName)
}

// validateFlatDestination checks that a flat destination is absolute, writable,
// and outside protected system locations. It also checks resolved symlinks.
func validateFlatDestination(destination string) error {
	trimmedDestination := strings.TrimSpace(destination)
	if trimmedDestination == "" {
		return errors.New("flat destination is required")
	}

	cleanDestination := filepath.Clean(trimmedDestination)
	if !filepath.IsAbs(cleanDestination) {
		return fmt.Errorf("flat destination %q must be absolute", cleanDestination)
	}

	if err := rejectDisallowedFlatDestination(cleanDestination); err != nil {
		return err
	}

	parent := filepath.Dir(cleanDestination)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("flat destination parent %q cannot be created: %w", parent, err)
	}

	if err := os.MkdirAll(cleanDestination, 0755); err != nil {
		return fmt.Errorf("flat destination %q cannot be created: %w", cleanDestination, err)
	}

	// Check the real target too, so a harmless-looking symlink cannot point into
	// a protected system directory.
	resolvedDestination, err := filepath.EvalSymlinks(cleanDestination)
	if err != nil {
		return fmt.Errorf("flat destination %q cannot be resolved: %w", cleanDestination, err)
	}

	if err := rejectDisallowedFlatDestination(resolvedDestination); err != nil {
		return err
	}

	// Creating and deleting a small temporary file proves the directory is writable.
	probe, err := os.CreateTemp(cleanDestination, ".portainer-write-test-*")
	if err != nil {
		return fmt.Errorf("flat destination %q is not writable: %w", cleanDestination, err)
	}

	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probeName)
		return fmt.Errorf("flat destination %q is not writable: %w", cleanDestination, err)
	}

	if err := os.Remove(probeName); err != nil {
		return fmt.Errorf("flat destination %q write test cleanup failed: %w", cleanDestination, err)
	}

	return nil
}

// rejectDisallowedFlatDestination rejects both a protected root and anything
// nested below it.
func rejectDisallowedFlatDestination(destination string) error {
	for _, disallowed := range disallowedFlatDestinations {
		if pathWithin(destination, disallowed) {
			return fmt.Errorf("flat destination %q is not allowed", destination)
		}
	}

	return nil
}

// pathWithin reports whether candidate is root itself or a descendant of root.
func pathWithin(candidate string, root string) bool {
	cleanRoot := filepath.Clean(root)
	if candidate == cleanRoot {
		return true
	}
	if cleanRoot == string(filepath.Separator) {
		return false
	}

	rel, err := filepath.Rel(cleanRoot, candidate)
	if err != nil {
		return false
	}

	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// prepareGitRepository selects the correct Git strategy. A plain deployment
// replaces the checkout; --keep and --source-dir synchronize through a fresh clone.
func prepareGitRepository(cmdCtx *exec.CommandExecutionContext, opts gitRepositoryOptions) error {
	sourceDir, err := cleanRepositorySourceDir(opts.sourceDir)
	if err != nil {
		log.Error().Err(err).Msg("Invalid Git repository source directory")
		return err
	}
	opts.sourceDir = sourceDir

	if opts.sourceDir != "" {
		return prepareGitRepositorySourceDir(cmdCtx, opts)
	}

	if !opts.keep {
		return cloneGitRepository(cmdCtx, opts)
	}

	// Determine which local changes a fresh repository must not overwrite.
	protectedPaths, err := protectedRepositoryPaths(opts.clonePath, opts.sourceDir, opts.unprotectMissingPaths)
	if err != nil {
		log.Error().Err(err).Msg("Failed to inspect existing Git repository")
		return exec.ErrDeployComposeFailure
	}

	// Clone separately instead of pulling into a possibly dirty working tree.
	tempMountPath, err := os.MkdirTemp("", "portainer-repo-*")
	if err != nil {
		log.Error().Err(err).Msg("Failed to create temporary Git repository directory")
		return exec.ErrDeployComposeFailure
	}
	// The deferred function runs when prepareGitRepository returns, including on
	// error. This is Go's common replacement for a Java finally block.
	defer func() {
		if err := os.RemoveAll(tempMountPath); err != nil {
			log.Warn().Err(err).Msg("Failed to remove temporary Git repository directory")
		}
	}()

	tempOpts := opts
	tempOpts.keep = false
	tempOpts.mountPath = tempMountPath
	tempOpts.clonePath = filesystem.JoinPaths(tempMountPath, "repo")

	if err := cloneGitRepository(cmdCtx, tempOpts); err != nil {
		return err
	}

	if err := syncRepositoryWorktree(tempOpts.clonePath, opts.clonePath, protectedPaths, opts.sourceDir); err != nil {
		log.Error().Err(err).Msg("Failed to synchronize Git repository")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

// prepareGitRepositorySourceDir clones a whole repository temporarily, then copies
// only sourceDir's contents to the destination root.
func prepareGitRepositorySourceDir(cmdCtx *exec.CommandExecutionContext, opts gitRepositoryOptions) error {
	protectedPaths := map[string]struct{}{}
	if opts.keep {
		var err error
		protectedPaths, err = protectedRepositoryPaths(opts.clonePath, opts.sourceDir, opts.unprotectMissingPaths)
		if err != nil {
			log.Error().Err(err).Msg("Failed to inspect existing Git repository")
			return exec.ErrDeployComposeFailure
		}
	} else if err := os.RemoveAll(opts.mountPath); err != nil {
		log.Error().Err(err).Msg("Failed to remove previous directory")
		return exec.ErrDeployComposeFailure
	}

	tempMountPath, err := os.MkdirTemp("", "portainer-repo-*")
	if err != nil {
		log.Error().Err(err).Msg("Failed to create temporary Git repository directory")
		return exec.ErrDeployComposeFailure
	}
	defer func() {
		if err := os.RemoveAll(tempMountPath); err != nil {
			log.Warn().Err(err).Msg("Failed to remove temporary Git repository directory")
		}
	}()

	tempOpts := opts
	tempOpts.keep = false
	tempOpts.sourceDir = ""
	tempOpts.mountPath = tempMountPath
	tempOpts.clonePath = filesystem.JoinPaths(tempMountPath, "repo")

	if err := cloneGitRepository(cmdCtx, tempOpts); err != nil {
		return err
	}

	if err := syncRepositoryWorktree(tempOpts.clonePath, opts.clonePath, protectedPaths, opts.sourceDir); err != nil {
		log.Error().Err(err).Msg("Failed to synchronize Git repository source directory")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

// cloneGitRepository replaces the old checkout with a shallow clone of one Git
// reference. It returns the shared deployment error so callers see one category.
func cloneGitRepository(cmdCtx *exec.CommandExecutionContext, opts gitRepositoryOptions) error {
	if _, err := os.Stat(opts.mountPath); err == nil {
		if err := os.RemoveAll(opts.mountPath); err != nil {
			log.Error().Err(err).Msg("Failed to remove previous directory")
			return exec.ErrDeployComposeFailure
		}
	}

	createPath := opts.mountPath
	if opts.mountPath == opts.clonePath {
		createPath = filepath.Dir(opts.mountPath)
	}

	if err := os.MkdirAll(createPath, 0755); err != nil {
		log.Error().Err(err).Msg("Failed to create destination directory")
		return exec.ErrDeployComposeFailure
	}

	log.Info().
		Str("directory", opts.mountPath).
		Msg("Creating target destination directory on disk")

	gitOptions := git.CloneOptions{
		URL:             opts.repository,
		ReferenceName:   plumbing.ReferenceName(opts.reference),
		Auth:            auth.GetAuth(opts.user, opts.password),
		Depth:           1,
		InsecureSkipTLS: opts.skipTLSVerify && fips.CanTLSSkipVerify(),
		Tags:            git.NoTags,
	}

	log.Info().
		Str("repository", opts.repository).
		Str("path", opts.clonePath).
		Str("url", gitOptions.URL).
		Int("depth", gitOptions.Depth).
		Msg("Cloning git repository")

	// Portainer's wrapper prevents the checkout from writing through symlinks.
	wt := portainergit.NewNoSymlinkFS(osfs.New(opts.clonePath))
	dot := osfs.New(filesystem.JoinPaths(opts.clonePath, ".git"))
	storer := gogitfs.NewStorage(dot, cache.NewObjectLRU(0))

	if _, err := git.CloneContext(cmdCtx.Context, storer, wt, &gitOptions); err != nil {
		log.Error().Err(err).Msg("Failed to clone Git repository")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

// protectedRepositoryPaths finds local files that synchronization must preserve:
// modified tracked files, missing tracked files, and untracked files.
func protectedRepositoryPaths(clonePath string, sourceDir string, unprotectMissingPaths map[string]struct{}) (map[string]struct{}, error) {
	if _, err := os.Stat(filesystem.JoinPaths(clonePath, ".git")); err != nil {
		if os.IsNotExist(err) {
			// Without Git metadata, every existing file is treated as user-owned.
			return allExistingFiles(clonePath)
		}

		return nil, err
	}

	repo, err := git.PlainOpen(clonePath)
	if err != nil {
		return nil, err
	}

	tracked, err := trackedFiles(repo, sourceDir)
	if err != nil {
		return nil, err
	}

	// A map with empty values is Go's lightweight equivalent of HashSet<String>.
	protected := map[string]struct{}{}
	for path, file := range tracked {
		dirty, missing, err := trackedFileDirty(clonePath, path, file)
		if err != nil {
			return nil, err
		}

		if missing {
			if _, unprotected := unprotectMissingPaths[path]; unprotected {
				continue
			}
		}

		if dirty {
			protected[path] = struct{}{}
		}
	}

	// Anything on disk that is absent from the commit is an untracked local file.
	if err := filepath.WalkDir(clonePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}

		if entry.IsDir() {
			return nil
		}

		relPath, err := relativeRepositoryPath(clonePath, path)
		if err != nil {
			return err
		}

		if _, ok := tracked[relPath]; !ok {
			protected[relPath] = struct{}{}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return protected, nil
}

// allExistingFiles returns every non-directory path below root as a protected set.
func allExistingFiles(root string) (map[string]struct{}, error) {
	// Start with an empty, non-nil set so callers can safely add or look up keys.
	protected := map[string]struct{}{}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return protected, nil
		}

		return nil, err
	}

	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		relPath, err := relativeRepositoryPath(root, path)
		if err != nil {
			return err
		}

		protected[relPath] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}

	return protected, nil
}

// trackedFiles reads the current Git commit and maps each included repository file
// to the path where it will appear in the destination.
func trackedFiles(repo *git.Repository, sourceDir string) (map[string]*object.File, error) {
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}

	files := map[string]*object.File{}
	iter, err := commit.Files()
	if err != nil {
		return nil, err
	}

	// The callback is similar to passing a Java lambda to forEach.
	if err := iter.ForEach(func(file *object.File) error {
		targetPath, ok := targetPathForRepositoryFile(file.Name, sourceDir)
		if ok {
			files[targetPath] = file
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return files, nil
}

// trackedFileDirty compares one file on disk with its committed Git blob. The
// booleans mean dirty and missing, in that order.
func trackedFileDirty(root string, targetPath string, file *object.File) (bool, bool, error) {
	currentPath := filesystem.JoinPaths(root, targetPath)
	info, err := os.Lstat(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, true, nil
		}

		return false, false, err
	}

	if !info.Mode().IsRegular() {
		return true, false, nil
	}

	currentFile, err := os.Open(currentPath)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = currentFile.Close() }()

	blobReader, err := file.Reader()
	if err != nil {
		return false, false, err
	}
	defer func() { _ = blobReader.Close() }()

	equal, err := readersEqual(currentFile, blobReader)
	if err != nil {
		return false, false, err
	}

	return !equal, false, nil
}

// targetPathForRepositoryFile maps a repository path to its destination path. A
// source directory is removed from the front because its contents are flattened.
func targetPathForRepositoryFile(repositoryPath string, sourceDir string) (string, bool) {
	if sourceDir == "" {
		return repositoryPath, true
	}

	prefix := sourceDir + "/"
	if !strings.HasPrefix(repositoryPath, prefix) {
		return "", false
	}

	targetPath := strings.TrimPrefix(repositoryPath, prefix)
	return targetPath, targetPath != ""
}

// readersEqual compares any two streams. io.Reader is a small Go interface that
// both os.File and go-git's blob reader satisfy automatically.
func readersEqual(left io.Reader, right io.Reader) (bool, error) {
	leftBytes, err := io.ReadAll(left)
	if err != nil {
		return false, err
	}

	rightBytes, err := io.ReadAll(right)
	if err != nil {
		return false, err
	}

	return bytes.Equal(leftBytes, rightBytes), nil
}

// syncRepositoryWorktree updates targetPath from a freshly cloned source while
// preserving protected local paths.
func syncRepositoryWorktree(sourcePath string, targetPath string, protectedPaths map[string]struct{}, sourceDir string) error {
	sourceRepo, err := git.PlainOpen(sourcePath)
	if err != nil {
		return err
	}

	sourceTracked, err := trackedFiles(sourceRepo, sourceDir)
	if err != nil {
		return err
	}

	// Remove files deleted in the new commit unless the user changed them locally.
	if _, err := os.Stat(filesystem.JoinPaths(targetPath, ".git")); err == nil {
		targetRepo, err := git.PlainOpen(targetPath)
		if err != nil {
			return err
		}

		targetTracked, err := trackedFiles(targetRepo, sourceDir)
		if err != nil {
			return err
		}

		for path := range targetTracked {
			if _, stillTracked := sourceTracked[path]; stillTracked {
				continue
			}

			if _, protected := protectedPaths[path]; protected {
				continue
			}

			if err := os.Remove(filesystem.JoinPaths(targetPath, path)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	sourceWorktreePath := sourcePath
	if sourceDir != "" {
		sourceWorktreePath = filesystem.JoinPaths(sourcePath, sourceDir)
	}
	if err := copyRepositoryFiles(sourceWorktreePath, targetPath, protectedPaths); err != nil {
		return err
	}

	// The destination should describe the fresh commit, so replace its Git metadata.
	if err := os.RemoveAll(filesystem.JoinPaths(targetPath, ".git")); err != nil {
		return err
	}

	return copyPath(filesystem.JoinPaths(sourcePath, ".git"), filesystem.JoinPaths(targetPath, ".git"))
}

// copyRepositoryFiles recursively copies a worktree but skips protected paths and
// the .git directory. Directories are created before their child files.
func copyRepositoryFiles(sourcePath string, targetPath string, protectedPaths map[string]struct{}) error {
	return filepath.WalkDir(sourcePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}

		relPath, err := relativeRepositoryPath(sourcePath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		target := filesystem.JoinPaths(targetPath, relPath)
		if entry.IsDir() {
			if protectedPath(protectedPaths, relPath) {
				return filepath.SkipDir
			}

			return os.MkdirAll(target, 0755)
		}

		if protectedPath(protectedPaths, relPath) || hasProtectedDescendant(protectedPaths, relPath) {
			return nil
		}

		return copyPath(path, target)
	})
}

// protectedPath performs a set lookup. The first map result is intentionally
// ignored with _, while the second reports whether the key exists.
func protectedPath(protectedPaths map[string]struct{}, path string) bool {
	_, protected := protectedPaths[path]
	return protected
}

// hasProtectedDescendant prevents replacing a parent path that contains a local
// file deeper in its directory tree.
func hasProtectedDescendant(protectedPaths map[string]struct{}, path string) bool {
	prefix := path + "/"
	for protectedPath := range protectedPaths {
		if strings.HasPrefix(protectedPath, prefix) {
			return true
		}
	}

	return false
}

// cleanRepositorySourceDir normalizes a repository-relative directory and rejects
// absolute paths or attempts to escape with `..`.
func cleanRepositorySourceDir(sourceDir string) (string, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return "", nil
	}

	if path.IsAbs(sourceDir) || filepath.VolumeName(sourceDir) != "" || strings.Contains(sourceDir, `\`) {
		return "", fmt.Errorf("source directory %q must be a relative repository path", sourceDir)
	}

	cleaned := path.Clean(sourceDir)
	if cleaned == "." {
		return "", nil
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("source directory %q cannot traverse outside the repository", sourceDir)
	}

	return cleaned, nil
}

// stripRepositorySourceDir converts a Compose path from repository coordinates to
// destination coordinates when sourceDir is flattened.
func stripRepositorySourceDir(filePath string, sourceDir string) (string, error) {
	sourceDir, err := cleanRepositorySourceDir(sourceDir)
	if err != nil {
		return "", err
	}

	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", errors.New("compose file path cannot be empty")
	}

	filePath = strings.TrimLeft(filePath, "/")
	if filepath.VolumeName(filePath) != "" || strings.Contains(filePath, `\`) {
		return "", fmt.Errorf("compose file path %q must be a relative repository path", filePath)
	}

	cleaned := path.Clean(filePath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("compose file path %q cannot traverse outside the repository", filePath)
	}

	if sourceDir == "" {
		return cleaned, nil
	}

	targetPath, ok := targetPathForRepositoryFile(cleaned, sourceDir)
	if ok {
		return targetPath, nil
	}

	return cleaned, nil
}

// copyPath copies a directory, regular file, or symbolic link while preserving
// file permissions. It replaces incompatible target file types when necessary.
func copyPath(source string, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}

	// Directories are copied recursively, much like walking a Java Files tree.
	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode()); err != nil {
			return err
		}

		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			if err := copyPath(filesystem.JoinPaths(source, entry.Name()), filesystem.JoinPaths(target, entry.Name())); err != nil {
				return err
			}
		}

		return nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}

	// A bitwise check identifies symbolic links in Go's FileMode value.
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(source)
		if err != nil {
			return err
		}

		if err := os.RemoveAll(target); err != nil {
			return err
		}

		return os.Symlink(linkTarget, target)
	}

	targetInfo, err := os.Lstat(target)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && (targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()

	// The | operator combines create, write-only, and truncate file flags.
	targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(targetFile, sourceFile)
	closeErr := targetFile.Close()
	if copyErr != nil {
		return copyErr
	}

	return closeErr
}

// relativeRepositoryPath returns a slash-separated path suitable for Git and for
// map keys, even when the operating system uses a different separator.
func relativeRepositoryPath(root string, path string) (string, error) {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(relPath), nil
}
