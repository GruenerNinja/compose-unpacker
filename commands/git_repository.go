package commands

import (
	"bytes"
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

type gitRepositoryOptions struct {
	repository    string
	reference     string
	user          string
	password      string
	skipTLSVerify bool
	keep          bool
	mountPath     string
	clonePath     string
	sourceDir     string
}

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
	filepath.Join(string(filepath.Separator), "var", "run"),
}

func gitRepositoryDeploymentPaths(destination string, projectName string, repositoryName string, flat bool) (string, string) {
	mountPath := exec.MakeWorkingDir(destination, projectName)
	clonePath := filesystem.JoinPaths(mountPath, repositoryName)
	if flat {
		mountPath = destination
		clonePath = destination
	}

	return mountPath, clonePath
}

func gitRepositoryMountPath(destination string, projectName string, flat bool) string {
	if flat {
		return destination
	}

	return exec.MakeWorkingDir(destination, projectName)
}

func validateFlatDestination(destination string) error {
	trimmedDestination := strings.TrimSpace(destination)
	if trimmedDestination == "" {
		return fmt.Errorf("flat destination is required")
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

	resolvedDestination, err := filepath.EvalSymlinks(cleanDestination)
	if err != nil {
		return fmt.Errorf("flat destination %q cannot be resolved: %w", cleanDestination, err)
	}

	if err := rejectDisallowedFlatDestination(resolvedDestination); err != nil {
		return err
	}

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

func rejectDisallowedFlatDestination(destination string) error {
	for _, disallowed := range disallowedFlatDestinations {
		if pathWithin(destination, disallowed) {
			return fmt.Errorf("flat destination %q is not allowed", destination)
		}
	}

	return nil
}

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

	protectedPaths, err := protectedRepositoryPaths(opts.clonePath, opts.sourceDir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to inspect existing Git repository")
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
	tempOpts.mountPath = tempMountPath
	tempOpts.clonePath = filepath.Join(tempMountPath, "repo")

	if err := cloneGitRepository(cmdCtx, tempOpts); err != nil {
		return err
	}

	if err := syncRepositoryWorktree(tempOpts.clonePath, opts.clonePath, protectedPaths, opts.sourceDir); err != nil {
		log.Error().Err(err).Msg("Failed to synchronize Git repository")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

func prepareGitRepositorySourceDir(cmdCtx *exec.CommandExecutionContext, opts gitRepositoryOptions) error {
	protectedPaths := map[string]struct{}{}
	if opts.keep {
		var err error
		protectedPaths, err = protectedRepositoryPaths(opts.clonePath, opts.sourceDir)
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
	tempOpts.clonePath = filepath.Join(tempMountPath, "repo")

	if err := cloneGitRepository(cmdCtx, tempOpts); err != nil {
		return err
	}

	if err := syncRepositoryWorktree(tempOpts.clonePath, opts.clonePath, protectedPaths, opts.sourceDir); err != nil {
		log.Error().Err(err).Msg("Failed to synchronize Git repository source directory")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

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

	wt := portainergit.NewNoSymlinkFS(osfs.New(opts.clonePath))
	dot := osfs.New(filesystem.JoinPaths(opts.clonePath, ".git"))
	storer := gogitfs.NewStorage(dot, cache.NewObjectLRU(0))

	if _, err := git.CloneContext(cmdCtx.Context, storer, wt, &gitOptions); err != nil {
		log.Error().Err(err).Msg("Failed to clone Git repository")
		return exec.ErrDeployComposeFailure
	}

	return nil
}

func protectedRepositoryPaths(clonePath string, sourceDir string) (map[string]struct{}, error) {
	if _, err := os.Stat(filepath.Join(clonePath, ".git")); err != nil {
		if os.IsNotExist(err) {
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

	protected := map[string]struct{}{}
	for path, file := range tracked {
		dirty, err := trackedFileDirty(clonePath, path, file)
		if err != nil {
			return nil, err
		}

		if dirty {
			protected[path] = struct{}{}
		}
	}

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

func allExistingFiles(root string) (map[string]struct{}, error) {
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

func trackedFileDirty(root string, targetPath string, file *object.File) (bool, error) {
	currentPath := filepath.Join(root, filepath.FromSlash(targetPath))
	info, err := os.Lstat(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}

		return false, err
	}

	if !info.Mode().IsRegular() {
		return true, nil
	}

	currentFile, err := os.Open(currentPath)
	if err != nil {
		return false, err
	}
	defer currentFile.Close()

	blobReader, err := file.Reader()
	if err != nil {
		return false, err
	}
	defer blobReader.Close()

	equal, err := readersEqual(currentFile, blobReader)
	if err != nil {
		return false, err
	}

	return !equal, nil
}

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

func syncRepositoryWorktree(sourcePath string, targetPath string, protectedPaths map[string]struct{}, sourceDir string) error {
	sourceRepo, err := git.PlainOpen(sourcePath)
	if err != nil {
		return err
	}

	sourceTracked, err := trackedFiles(sourceRepo, sourceDir)
	if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
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

			if err := os.Remove(filepath.Join(targetPath, filepath.FromSlash(path))); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	sourceWorktreePath := filepath.Join(sourcePath, filepath.FromSlash(sourceDir))
	if err := copyRepositoryFiles(sourceWorktreePath, targetPath, protectedPaths); err != nil {
		return err
	}

	if err := os.RemoveAll(filepath.Join(targetPath, ".git")); err != nil {
		return err
	}

	return copyPath(filepath.Join(sourcePath, ".git"), filepath.Join(targetPath, ".git"))
}

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

		target := filepath.Join(targetPath, filepath.FromSlash(relPath))
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

func protectedPath(protectedPaths map[string]struct{}, path string) bool {
	_, protected := protectedPaths[path]
	return protected
}

func hasProtectedDescendant(protectedPaths map[string]struct{}, path string) bool {
	prefix := path + "/"
	for protectedPath := range protectedPaths {
		if strings.HasPrefix(protectedPath, prefix) {
			return true
		}
	}

	return false
}

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

func stripRepositorySourceDir(filePath string, sourceDir string) (string, error) {
	sourceDir, err := cleanRepositorySourceDir(sourceDir)
	if err != nil {
		return "", err
	}

	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("compose file path cannot be empty")
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

func copyPath(source string, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode()); err != nil {
			return err
		}

		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}

		return nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}

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
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer targetFile.Close()

	_, err = io.Copy(targetFile, sourceFile)
	return err
}

func relativeRepositoryPath(root string, path string) (string, error) {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(relPath), nil
}
