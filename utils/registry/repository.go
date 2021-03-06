/*
   Copyright (c) 2016 VMware, Inc. All Rights Reserved.
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/docker/distribution/manifest/schema1"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/vmware/harbor/utils/log"
	"github.com/vmware/harbor/utils/registry/auth"
	"github.com/vmware/harbor/utils/registry/errors"
)

// Repository holds information of a repository entity
type Repository struct {
	Name     string
	Endpoint *url.URL
	client   *http.Client
}

// TODO add agent to header of request, notifications need it

// NewRepository returns an instance of Repository
func NewRepository(name, endpoint string, client *http.Client) (*Repository, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimRight(endpoint, "/")

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	repository := &Repository{
		Name:     name,
		Endpoint: u,
		client:   client,
	}

	return repository, nil
}

// NewRepositoryWithCredential returns a Repository instance which will authorize the request
// according to the credenttial
func NewRepositoryWithCredential(name, endpoint string, credential auth.Credential) (*Repository, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimRight(endpoint, "/")

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	client, err := newClient(endpoint, "", credential, "repository", name, "pull", "push")
	if err != nil {
		return nil, err
	}

	repository := &Repository{
		Name:     name,
		Endpoint: u,
		client:   client,
	}

	log.Debugf("initialized a repository client with credential: %s %s", endpoint, name)

	return repository, nil
}

// NewRepositoryWithUsername returns a Repository instance which will authorize the request
// according to the privileges of user
func NewRepositoryWithUsername(name, endpoint, username string) (*Repository, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimRight(endpoint, "/")

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	client, err := newClient(endpoint, username, nil, "repository", name, "pull", "push")

	repository := &Repository{
		Name:     name,
		Endpoint: u,
		client:   client,
	}

	log.Debugf("initialized a repository client with username: %s %s %s", endpoint, name, username)

	return repository, nil
}

// try to convert err to errors.Error if it is
func isUnauthorizedError(err error) (bool, error) {
	if strings.Contains(err.Error(), http.StatusText(http.StatusUnauthorized)) {
		return true, errors.Error{
			StatusCode: http.StatusUnauthorized,
			StatusText: http.StatusText(http.StatusUnauthorized),
		}
	}
	return false, err
}

// ListTag ...
func (r *Repository) ListTag() ([]string, error) {
	tags := []string{}
	req, err := http.NewRequest("GET", buildTagListURL(r.Endpoint.String(), r.Name), nil)
	if err != nil {
		return tags, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			return tags, e
		}
		return tags, err
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return tags, err
	}

	if resp.StatusCode == http.StatusOK {
		tagsResp := struct {
			Tags []string `json:"tags"`
		}{}

		if err := json.Unmarshal(b, &tagsResp); err != nil {
			return tags, err
		}

		tags = tagsResp.Tags

		return tags, nil
	}
	return tags, errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}

}

// ManifestExist ...
func (r *Repository) ManifestExist(reference string) (digest string, exist bool, err error) {
	req, err := http.NewRequest("HEAD", buildManifestURL(r.Endpoint.String(), r.Name, reference), nil)
	if err != nil {
		return
	}

	req.Header.Add(http.CanonicalHeaderKey("Accept"), schema1.MediaTypeManifest)
	req.Header.Add(http.CanonicalHeaderKey("Accept"), schema2.MediaTypeManifest)

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			err = e
			return
		}
		return
	}

	if resp.StatusCode == http.StatusOK {
		exist = true
		digest = resp.Header.Get(http.CanonicalHeaderKey("Docker-Content-Digest"))
		return
	}

	if resp.StatusCode == http.StatusNotFound {
		return
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	err = errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}
	return
}

// PullManifest ...
func (r *Repository) PullManifest(reference string, acceptMediaTypes []string) (digest, mediaType string, payload []byte, err error) {
	req, err := http.NewRequest("GET", buildManifestURL(r.Endpoint.String(), r.Name, reference), nil)
	if err != nil {
		return
	}

	for _, mediaType := range acceptMediaTypes {
		req.Header.Add(http.CanonicalHeaderKey("Accept"), mediaType)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			err = e
			return
		}
		return
	}

	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	if resp.StatusCode == http.StatusOK {
		digest = resp.Header.Get(http.CanonicalHeaderKey("Docker-Content-Digest"))
		mediaType = resp.Header.Get(http.CanonicalHeaderKey("Content-Type"))
		payload = b
		return
	}

	err = errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}

	return
}

// PushManifest ...
func (r *Repository) PushManifest(reference, mediaType string, payload []byte) (digest string, err error) {
	req, err := http.NewRequest("PUT", buildManifestURL(r.Endpoint.String(), r.Name, reference),
		bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set(http.CanonicalHeaderKey("Content-Type"), mediaType)

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			err = e
			return
		}
		return
	}

	if resp.StatusCode == http.StatusCreated {
		digest = resp.Header.Get(http.CanonicalHeaderKey("Docker-Content-Digest"))
		return
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	err = errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}

	return
}

// DeleteManifest ...
func (r *Repository) DeleteManifest(digest string) error {
	req, err := http.NewRequest("DELETE", buildManifestURL(r.Endpoint.String(), r.Name, digest), nil)
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			return e
		}
		return err
	}

	if resp.StatusCode == http.StatusAccepted {
		return nil
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}
}

// DeleteTag ...
func (r *Repository) DeleteTag(tag string) error {
	digest, exist, err := r.ManifestExist(tag)
	if err != nil {
		return err
	}

	if !exist {
		return errors.Error{
			StatusCode: http.StatusNotFound,
			StatusText: http.StatusText(http.StatusNotFound),
		}
	}

	return r.DeleteManifest(digest)
}

// BlobExist ...
func (r *Repository) BlobExist(digest string) (bool, error) {
	req, err := http.NewRequest("HEAD", buildBlobURL(r.Endpoint.String(), r.Name, digest), nil)
	if err != nil {
		return false, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			return false, e
		}
		return false, err
	}

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	return false, errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}
}

// PullBlob ...
func (r *Repository) PullBlob(digest string) (size int64, data []byte, err error) {
	req, err := http.NewRequest("GET", buildBlobURL(r.Endpoint.String(), r.Name, digest), nil)
	if err != nil {
		return
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			err = e
			return
		}
		return
	}

	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	if resp.StatusCode == http.StatusOK {
		contengLength := resp.Header.Get(http.CanonicalHeaderKey("Content-Length"))
		size, err = strconv.ParseInt(contengLength, 10, 64)
		if err != nil {
			return
		}
		data = b
		return
	}

	err = errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}

	return
}

func (r *Repository) initiateBlobUpload(name string) (location, uploadUUID string, err error) {
	req, err := http.NewRequest("POST", buildInitiateBlobUploadURL(r.Endpoint.String(), r.Name), nil)
	req.Header.Set(http.CanonicalHeaderKey("Content-Length"), "0")

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			err = e
			return
		}
		return
	}

	if resp.StatusCode == http.StatusAccepted {
		location = resp.Header.Get(http.CanonicalHeaderKey("Location"))
		uploadUUID = resp.Header.Get(http.CanonicalHeaderKey("Docker-Upload-UUID"))
		return
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	err = errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}

	return
}

func (r *Repository) monolithicBlobUpload(location, digest string, size int64, data []byte) error {
	req, err := http.NewRequest("PUT", buildMonolithicBlobUploadURL(location, digest), bytes.NewReader(data))
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			return e
		}
		return err
	}

	if resp.StatusCode == http.StatusCreated {
		return nil
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}
}

// PushBlob ...
func (r *Repository) PushBlob(digest string, size int64, data []byte) error {
	exist, err := r.BlobExist(digest)
	if err != nil {
		return err
	}

	if exist {
		log.Infof("blob already exists, skip pushing: %s %s", r.Name, digest)
		return nil
	}

	location, _, err := r.initiateBlobUpload(r.Name)
	if err != nil {
		return err
	}

	return r.monolithicBlobUpload(location, digest, size, data)
}

// DeleteBlob ...
func (r *Repository) DeleteBlob(digest string) error {
	req, err := http.NewRequest("DELETE", buildBlobURL(r.Endpoint.String(), r.Name, digest), nil)
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		ok, e := isUnauthorizedError(err)
		if ok {
			return e
		}
		return err
	}

	if resp.StatusCode == http.StatusAccepted {
		return nil
	}

	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return errors.Error{
		StatusCode: resp.StatusCode,
		StatusText: resp.Status,
		Message:    string(b),
	}
}

func buildPingURL(endpoint string) string {
	return fmt.Sprintf("%s/v2/", endpoint)
}

func buildTagListURL(endpoint, repoName string) string {
	return fmt.Sprintf("%s/v2/%s/tags/list", endpoint, repoName)
}

func buildManifestURL(endpoint, repoName, reference string) string {
	return fmt.Sprintf("%s/v2/%s/manifests/%s", endpoint, repoName, reference)
}

func buildBlobURL(endpoint, repoName, reference string) string {
	return fmt.Sprintf("%s/v2/%s/blobs/%s", endpoint, repoName, reference)
}

func buildInitiateBlobUploadURL(endpoint, repoName string) string {
	return fmt.Sprintf("%s/v2/%s/blobs/uploads/", endpoint, repoName)
}

func buildMonolithicBlobUploadURL(location, digest string) string {
	return fmt.Sprintf("%s&digest=%s", location, digest)
}
