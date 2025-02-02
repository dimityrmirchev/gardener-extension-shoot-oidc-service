// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package imagevector

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/strings/slices"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	versionutils "github.com/gardener/gardener/pkg/utils/version"
)

const (
	// OverrideEnv is the name of the image vector override environment variable.
	OverrideEnv = "IMAGEVECTOR_OVERWRITE"
	// SHA256TagPrefix is the prefix in an image tag for sha256 tags.
	SHA256TagPrefix = "sha256:"
)

// Read reads an ImageVector from the given io.Reader.
func Read(r io.Reader) (ImageVector, error) {
	vector := struct {
		Images ImageVector `json:"images" yaml:"images"`
	}{}

	if err := yaml.NewDecoder(r).Decode(&vector); err != nil {
		return nil, err
	}

	if errs := ValidateImageVector(vector.Images, field.NewPath("images")); len(errs) > 0 {
		return nil, errs.ToAggregate()
	}

	return vector.Images, nil
}

// ReadFile reads an ImageVector from the file with the given name.
func ReadFile(name string) (ImageVector, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return Read(file)
}

// ReadGlobalImageVectorWithEnvOverride reads the global image vector and applies the env override. Exposed for testing.
func ReadGlobalImageVectorWithEnvOverride(filePath string) (ImageVector, error) {
	imageVector, err := ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	return WithEnvOverride(imageVector)
}

// mergeImageSources merges the two given ImageSources.
//
// If the tag of the override is non-empty, it immediately returns the override.
// Otherwise, the override is copied, gets the tag of the old source and is returned.
func mergeImageSources(old, override *ImageSource) *ImageSource {
	tag := override.Tag
	if tag == nil {
		tag = old.Tag
	}

	runtimeVersion := override.RuntimeVersion
	if runtimeVersion == nil {
		runtimeVersion = old.RuntimeVersion
	}

	targetVersion := override.TargetVersion
	if targetVersion == nil {
		targetVersion = old.TargetVersion
	}

	architectures := override.Architectures
	if architectures == nil {
		architectures = old.Architectures
	}

	return &ImageSource{
		Name:           override.Name,
		RuntimeVersion: runtimeVersion,
		TargetVersion:  targetVersion,
		Architectures:  architectures,
		Repository:     override.Repository,
		Tag:            tag,
	}
}

type imageSourceKey struct {
	Name           string
	RuntimeVersion string
	TargetVersion  string
	Architectures  [32]byte
}

func computeKey(source *ImageSource) imageSourceKey {
	var (
		runtimeVersion, targetVersion string
		architectures                 [32]byte
	)

	if source.RuntimeVersion != nil {
		runtimeVersion = *source.RuntimeVersion
	}
	if source.TargetVersion != nil {
		targetVersion = *source.TargetVersion
	}
	if source.Architectures != nil {
		archs := strings.Join(source.Architectures, "")
		architectures = sha256.Sum256([]byte(archs))
	}

	return imageSourceKey{
		Name:           source.Name,
		RuntimeVersion: runtimeVersion,
		TargetVersion:  targetVersion,
		Architectures:  architectures,
	}
}

// Merge merges the given ImageVectors into one.
//
// Images of ImageVectors that are later in the given sequence with the same name override
// previous images.
func Merge(vectors ...ImageVector) ImageVector {
	var (
		out        ImageVector
		keyToIndex = make(map[imageSourceKey]int)
	)

	for _, vector := range vectors {
		for _, image := range vector {
			key := computeKey(image)

			if idx, ok := keyToIndex[key]; ok {
				out[idx] = mergeImageSources(out[idx], image)
				continue
			}

			keyToIndex[key] = len(out)
			out = append(out, image)
		}
	}

	return out
}

// WithEnvOverride checks if an environment variable with the key IMAGEVECTOR_OVERWRITE is set.
// If yes, it reads the ImageVector at the value of the variable and merges it with the given one.
// Otherwise, it returns the unmodified ImageVector.
func WithEnvOverride(vector ImageVector) (ImageVector, error) {
	overwritePath := os.Getenv(OverrideEnv)
	if len(overwritePath) == 0 {
		return vector, nil
	}

	override, err := ReadFile(overwritePath)
	if err != nil {
		return nil, err
	}

	return Merge(vector, override), nil
}

// String implements Stringer.
func (o *FindOptions) String() string {
	var runtimeVersion string
	if o.RuntimeVersion != nil {
		runtimeVersion = "runtime version " + *o.RuntimeVersion + " "
	}

	var targetVersion string
	if o.TargetVersion != nil {
		targetVersion = "target version " + *o.TargetVersion + " "
	}

	var architecture string
	if o.Architecture != nil {
		architecture = "architecture " + *o.Architecture
	}

	return runtimeVersion + targetVersion + architecture
}

// ApplyOptions applies the given FindOptionFuncs to these FindOptions. Returns a pointer to the mutated value.
func (o *FindOptions) ApplyOptions(opts []FindOptionFunc) *FindOptions {
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// RuntimeVersion sets the RuntimeVersion of the FindOptions to the given version.
func RuntimeVersion(version string) FindOptionFunc {
	return func(options *FindOptions) {
		options.RuntimeVersion = &version
	}
}

// TargetVersion sets the TargetVersion of the FindOptions to the given version.
func TargetVersion(version string) FindOptionFunc {
	return func(options *FindOptions) {
		options.TargetVersion = &version
	}
}

// Architecture sets the Architecture of the FindOptions to the given arch.
func Architecture(arch string) FindOptionFunc {
	return func(options *FindOptions) {
		options.Architecture = &arch
	}
}

var r = regexp.MustCompile(`^(v?[0-9]+\.[0-9]+\.[0-9]+|=)`)

func checkVersionConstraint(constraint, version *string) (score int, ok bool, err error) {
	if constraint == nil || version == nil {
		return 0, true, nil
	}

	matches, err := versionutils.CheckVersionMeetsConstraint(*version, *constraint)
	if err != nil || !matches {
		return 0, false, err
	}

	score = 1

	// prioritize equal constraints
	if r.MatchString(*constraint) {
		score = 2
	}

	return score, true, nil
}

func checkArchitectureConstraint(source []string, desired *string) (score int, ok bool, err error) {
	// if image doesn't have a architecture tag it is considered as multi arch image
	// and if worker pool machine doesn't have architecture tag it is by default considered amd64 machine.
	var sourceArch, desiredArch = []string{v1beta1constants.ArchitectureAMD64, v1beta1constants.ArchitectureARM64}, v1beta1constants.ArchitectureAMD64

	if source != nil {
		sourceArch = source
	}
	if desired != nil {
		desiredArch = *desired
	}

	if len(sourceArch) > 1 && slices.Contains(sourceArch, desiredArch) {
		return 1, true, nil
	}
	if len(sourceArch) == 1 && slices.Contains(sourceArch, desiredArch) {
		// prioritize equal constraints
		return 2, true, nil
	}

	return
}

func match(source *ImageSource, name string, opts *FindOptions) (score int, ok bool, err error) {
	if source.Name != name {
		return 0, false, nil
	}

	runtimeScore, ok, err := checkVersionConstraint(source.RuntimeVersion, opts.RuntimeVersion)
	if err != nil || !ok {
		return 0, false, err
	}
	score += runtimeScore

	targetScore, ok, err := checkVersionConstraint(source.TargetVersion, opts.TargetVersion)
	if err != nil || !ok {
		return 0, false, err
	}
	score += targetScore

	archScore, ok, err := checkArchitectureConstraint(source.Architectures, opts.Architecture)
	if err != nil || !ok {
		return 0, false, err
	}
	score += archScore

	return score, true, nil
}

// FindImage returns an image with the given <name> from the sources in the image vector.
// The <k8sVersion> specifies the kubernetes version the image will be running on.
// The <targetK8sVersion> specifies the kubernetes version the image shall target.
// If multiple entries were found, the provided <k8sVersion> is compared with the constraints
// stated in the image definition.
// In case multiple images match the search, the first which was found is returned.
// In case no image was found, an error is returned.
func (v ImageVector) FindImage(name string, opts ...FindOptionFunc) (*Image, error) {
	o := &FindOptions{}
	o = o.ApplyOptions(opts)

	var (
		bestScore     int
		bestCandidate *ImageSource
	)

	for _, source := range v {
		if source.Name == name {
			score, ok, err := match(source, name, o)
			if err != nil {
				return nil, err
			}

			if ok && (bestCandidate == nil || score > bestScore) {
				bestCandidate = source
				bestScore = score
			}
		}
	}

	if bestCandidate == nil {
		return nil, fmt.Errorf("could not find image %q opts %v", name, o)
	}

	return bestCandidate.ToImage(o.TargetVersion), nil
}

// FindImages returns an image map with the given <names> from the sources in the image vector.
// The <k8sVersion> specifies the kubernetes version the image will be running on.
// The <targetK8sVersion> specifies the kubernetes version the image shall target.
// If multiple entries were found, the provided <k8sVersion> is compared with the constraints
// stated in the image definition.
// In case multiple images match the search, the first which was found is returned.
// In case no image was found, an error is returned.
func FindImages(v ImageVector, names []string, opts ...FindOptionFunc) (map[string]*Image, error) {
	images := map[string]*Image{}
	for _, imageName := range names {
		image, err := v.FindImage(imageName, opts...)
		if err != nil {
			return nil, err
		}
		images[imageName] = image
	}
	return images, nil
}

// ToImage applies the given <targetK8sVersion> to the source to produce an output image.
// If the tag of an image source is empty, it will use the given <targetVersion> as tag.
func (i *ImageSource) ToImage(targetVersion *string) *Image {
	tag := i.Tag
	if tag == nil && targetVersion != nil {
		version := fmt.Sprintf("v%s", strings.TrimLeft(*targetVersion, "v"))
		tag = &version
	}

	return &Image{
		Name:       i.Name,
		Repository: i.Repository,
		Tag:        tag,
	}
}

// String will returns the string representation of the image.
func (i *Image) String() string {
	if i.Tag == nil {
		return i.Repository
	}

	delimiter := ":"
	if strings.HasPrefix(*i.Tag, SHA256TagPrefix) {
		delimiter = "@"
	}

	return i.Repository + delimiter + *i.Tag
}

// ImageMapToValues transforms the given image name to image mapping into chart Values.
func ImageMapToValues(m map[string]*Image) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v.String()
	}
	return out
}
