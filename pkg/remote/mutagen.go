package remote

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	mutagenConfig "bunnyshell.com/dev/pkg/mutagen/config"
	"bunnyshell.com/dev/pkg/util"
	"gopkg.in/yaml.v3"
)

const (
	MutagenVersion = "v0.15.3"

	mutagenBinFilename      = "mutagen"
	mutagenConfigFilename   = "mutagen.yaml"
	mutagenDownloadFilename = "mutagen_%s_%s_%s.tar.gz"
	mutagenDownloadUrl      = "https://github.com/mutagen-io/mutagen/releases/download/%s/%s"
)

func (r *RemoteDevelopment) ensureMutagen() error {
	r.StartSpinner(" Setup Mutagen")
	defer r.StopSpinner()

	if err := ensureMutagenBin(); err != nil {
		return err
	}

	return ensureMutagenConfigFile()
}

func ensureMutagenConfigFile() error {
	mutagenConfigFilePath, err := getMutagenConfigFilePath()
	if err != nil {
		return err
	}

	enableVCS := true
	ignore := mutagenConfig.NewIgnore().WithVCS(&enableVCS).WithPaths([]string{
		"node_modules",
		"vendor",
	})
	defaults := mutagenConfig.NewSyncDefaults().WithMode(mutagenConfig.OneWayReplica).WithIgnore(ignore)
	sync := mutagenConfig.NewSync().WithDefaults(defaults)
	config := mutagenConfig.NewConfiguration().WithSync(sync)

	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	return os.WriteFile(mutagenConfigFilePath, data, 0644)
}

func (r *RemoteDevelopment) startMutagenSession() error {
	r.StartSpinner(" Start Mutagen Session")
	defer r.StopSpinner()

	mutagenBinPath, err := getMutagenBinPath()
	if err != nil {
		return err
	}
	mutagenConfigFilePath, err := getMutagenConfigFilePath()
	if err != nil {
		return err
	}

	mutagenArgs := []string{
		"sync",
		"create",
		"-n", r.getMutagenSessionName(),
		"--no-global-configuration",
		"-c", mutagenConfigFilePath,
		r.localSyncPath,
		fmt.Sprintf(
			"%s:%s",
			r.getSSHHostname(),
			r.remoteSyncPath,
		),
	}

	mutagenCmd := exec.Command(mutagenBinPath, mutagenArgs...)
	_, err = mutagenCmd.CombinedOutput()

	return err
}

func (r *RemoteDevelopment) terminateMutagenSession() error {
	mutagenBinPath, err := getMutagenBinPath()
	if err != nil {
		return err
	}

	mutagenArgs := []string{
		"sync",
		"terminate",
		r.getMutagenSessionName(),
	}

	mutagenCmd := exec.Command(mutagenBinPath, mutagenArgs...)
	mutagenCmd.Run()

	return nil
}

func (r *RemoteDevelopment) terminateMutagenDaemon() error {
	mutagenBinPath, err := getMutagenBinPath()
	if err != nil {
		return err
	}

	mutagenArgs := []string{
		"daemon",
		"stop",
	}

	mutagenCmd := exec.Command(mutagenBinPath, mutagenArgs...)
	mutagenCmd.Run()

	return nil
}

func (r *RemoteDevelopment) getMutagenSessionName() string {
	return fmt.Sprintf("rd-%s", r.getMutagenSessionKey()[:16])
}

func (r *RemoteDevelopment) getMutagenSessionKey() string {
	plaintext := fmt.Sprintf("%s-%s-%s", r.remoteSyncPath, r.deployment.GetName(), r.deployment.GetNamespace())
	hash := md5.Sum([]byte(plaintext))
	return hex.EncodeToString(hash[:])
}

func getMutagenBinPath() (string, error) {
	workspaceDir, err := util.GetRemoteDevWorkspaceDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(workspaceDir, mutagenBinFilename), nil
}

func getMutagenConfigFilePath() (string, error) {
	workspaceDir, err := util.GetRemoteDevWorkspaceDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(workspaceDir, mutagenConfigFilename), nil
}

func ensureMutagenBin() error {
	mutagenBinPath, err := getMutagenBinPath()
	if err != nil {
		return err
	}

	stats, err := os.Stat(mutagenBinPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && stats.Size() > 0 && !stats.IsDir() {
		return nil
	}

	downloadFilename := fmt.Sprintf(mutagenDownloadFilename, runtime.GOOS, runtime.GOARCH, MutagenVersion)
	mutagenArchivePath := filepath.Dir(mutagenBinPath) + "/" + downloadFilename
	downloadUrl := fmt.Sprintf(mutagenDownloadUrl, MutagenVersion, downloadFilename)

	err = downloadMutagenArchive(downloadUrl, mutagenArchivePath)
	if err != nil {
		return err
	}

	err = extractMutagenBin(mutagenArchivePath, mutagenBinPath)
	if err != nil {
		return err
	}

	return removeMutagenArchive(mutagenArchivePath)
}

func removeMutagenArchive(filePath string) error {
	return os.Remove(filePath)
}

func downloadMutagenArchive(source, destination string) error {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := client.Get(source)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func extractMutagenBin(source, destination string) error {
	return extractMutagenBinTarGz(source, destination)
}

func extractMutagenBinTarGz(source, destination string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}

	gzipReader, err := gzip.NewReader(sourceFile)
	if err != nil {
		return err
	}

	tarReader := tar.NewReader(gzipReader)

	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		if header.Name == getMutagenBinFilename() {
			destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			defer destinationFile.Close()

			if _, err := io.Copy(destinationFile, tarReader); err != nil {
				return err
			}
			return nil
		}
	}

	return nil
}
