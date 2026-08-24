/*
Copyright 2025 The Kubernetes Authors.

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

package kubedeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldOwner is what this package's writes are attributed to, so that a
// re-deploy updates what it wrote last time and leaves an operator's own edits
// alone rather than fighting over them.
const FieldOwner = client.FieldOwner("kcp-cluster-api-deploy")

// Apply creates or updates every object, in the order Objects returns them.
//
// Server-side apply rather than create-or-update: a second run of `deploy` is
// the normal way to change an installation, and apply is the only write that
// converges without reading first and without clobbering a field somebody else
// set.
func Apply(ctx context.Context, cl client.Client, objects []client.Object, log logr.Logger) error {
	for _, obj := range objects {
		if err := cl.Patch(ctx, obj, client.Apply, FieldOwner, client.ForceOwnership); err != nil {
			return fmt.Errorf("applying %s %s/%s: %w",
				obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err)
		}
		log.V(1).Info("Applied",
			"kind", obj.GetObjectKind().GroupVersionKind().Kind,
			"namespace", obj.GetNamespace(), "name", obj.GetName())
	}
	return nil
}

// WaitForStatefulSet waits until every replica is ready.
func WaitForStatefulSet(ctx context.Context, cl client.Client, namespace, name string, timeout time.Duration) error {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	var last string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		set := &appsv1.StatefulSet{}
		if err := cl.Get(ctx, key, set); err != nil {
			last = err.Error()
			return false, nil //nolint:nilerr // transient while the object is being created.
		}
		want := int32(1)
		if set.Spec.Replicas != nil {
			want = *set.Spec.Replicas
		}
		last = fmt.Sprintf("%d/%d ready", set.Status.ReadyReplicas, want)
		return set.Status.ReadyReplicas >= want, nil
	})
	if err != nil {
		return fmt.Errorf("StatefulSet %s/%s was not ready after %s (%s): %w", namespace, name, timeout, last, err)
	}
	return nil
}

// WaitForDeployment waits until the Deployment has an available replica.
func WaitForDeployment(ctx context.Context, cl client.Client, namespace, name string, timeout time.Duration) error {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	var last string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := cl.Get(ctx, key, deployment); err != nil {
			last = err.Error()
			return false, nil //nolint:nilerr // transient while the object is being created.
		}
		last = fmt.Sprintf("%d available", deployment.Status.AvailableReplicas)
		return deployment.Status.AvailableReplicas >= 1, nil
	})
	if err != nil {
		return fmt.Errorf("Deployment %s/%s had no available replica after %s (%s): %w", namespace, name, timeout, last, err)
	}
	return nil
}

// JobResult is how a Job finished.
type JobResult struct {
	Succeeded bool

	// Reason is what the Job's own conditions say when it failed, which is the
	// difference between "the demo reported a leak" and "the pod could not
	// pull its image".
	Reason string
}

// WaitForJob waits until a Job has either completed or failed.
func WaitForJob(ctx context.Context, cl client.Client, namespace, name string, timeout time.Duration) (JobResult, error) {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	var result JobResult
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		job := &batchv1.Job{}
		if err := cl.Get(ctx, key, job); err != nil {
			return false, nil //nolint:nilerr // transient while the object is being created.
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobComplete:
				result = JobResult{Succeeded: true}
				return true, nil
			case batchv1.JobFailed:
				result = JobResult{Reason: fmt.Sprintf("%s: %s", condition.Reason, condition.Message)}
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return result, fmt.Errorf("Job %s/%s neither completed nor failed within %s: %w", namespace, name, timeout, err)
	}
	return result, nil
}

// StreamJobLogs follows a Job's pod and copies its output to out.
//
// The demo's whole result is what it prints - the status tables, the onboarding
// table, what each tenant may read - so a run whose logs stayed in the cluster
// would have shown nothing. This is what makes `deploy` report the same thing
// `task demo` does, from a pod instead of from a process.
//
// It returns when the log stream ends, which is when the pod's container
// exits. Whether that was a success is WaitForJob's answer, not this one's.
func StreamJobLogs(
	ctx context.Context,
	cl client.Client,
	clientset kubernetes.Interface,
	namespace, name string,
	out io.Writer,
	timeout time.Duration,
) error {
	pod, err := waitForJobPod(ctx, cl, namespace, name, timeout)
	if err != nil {
		return err
	}

	stream, err := clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return fmt.Errorf("following the logs of %s/%s: %w", namespace, pod, err)
	}
	defer stream.Close() //nolint:errcheck // nothing to do with a failure to close a read stream.

	if _, err := io.Copy(out, stream); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("reading the logs of %s/%s: %w", namespace, pod, err)
	}
	return nil
}

// waitForJobPod returns the name of the Job's pod once its container can
// produce logs.
//
// Pending is not enough: asking for the logs of a pod whose container has not
// started fails with "is waiting to start", and treating that as the stream
// ending would report a demo that printed nothing.
func waitForJobPod(ctx context.Context, cl client.Client, namespace, name string, timeout time.Duration) (string, error) {
	var (
		pod  string
		last string
	)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pods := &corev1.PodList{}
		if err := cl.List(ctx, pods,
			client.InNamespace(namespace),
			client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(labels.Set{"job-name": name})},
		); err != nil {
			last = err.Error()
			return false, nil //nolint:nilerr // transient while the pod is being created.
		}
		for _, candidate := range pods.Items {
			last = string(candidate.Status.Phase)
			for _, status := range candidate.Status.ContainerStatuses {
				if status.State.Waiting != nil {
					last = fmt.Sprintf("%s: %s", candidate.Status.Phase, status.State.Waiting.Reason)
					continue
				}
				pod = candidate.Name
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("no pod of Job %s/%s started within %s (%s): %w", namespace, name, timeout, last, err)
	}
	return pod, nil
}

// Delete removes an installation: the namespace and everything in it.
//
// The namespace rather than the objects one by one, because that is what also
// removes what this package did not create - the shard's PersistentVolumeClaim
// among them, which a StatefulSet leaves behind on purpose and which would
// otherwise make the next deploy adopt the last one's etcd data and the
// certificates it no longer has the keys for.
func Delete(ctx context.Context, cl client.Client, namespace string, timeout time.Duration) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if err := cl.Delete(ctx, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting namespace %s: %w", namespace, err)
	}
	if timeout <= 0 {
		return nil
	}

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		err := cl.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{})
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		return fmt.Errorf("namespace %s was still terminating after %s: %w", namespace, timeout, err)
	}
	return nil
}

// ExistingCredentials returns the credentials an installation is already
// running with, and whether there is an installation at all.
func ExistingCredentials(ctx context.Context, cl client.Client, namespace string) (Credentials, bool, error) {
	read := func(name string) (*corev1.Secret, bool, error) {
		secret := &corev1.Secret{}
		err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret)
		switch {
		case err == nil:
			return secret, true, nil
		case apierrors.IsNotFound(err):
			return nil, false, nil
		default:
			return nil, false, fmt.Errorf("reading Secret %s/%s: %w", namespace, name, err)
		}
	}

	var secrets [3]*corev1.Secret
	for i, name := range []string{KubeconfigSecretName, ServingSecretName, ClientCASecretName} {
		secret, found, err := read(name)
		if err != nil {
			return Credentials{}, false, err
		}
		if !found {
			return Credentials{}, false, nil
		}
		secrets[i] = secret
	}

	creds, err := LoadCredentials(secrets[0], secrets[1], secrets[2])
	if err != nil {
		return Credentials{}, false, err
	}
	return creds, true, nil
}

// ReplaceJob deletes a Job of the same name and waits for it to be gone.
//
// A Job's pod template is immutable, so applying one over another fails rather
// than converging - and a second demo run wants a second run, not the first
// one's result read again. Its pods go with it (foreground propagation), so
// the next run's logs are its own.
func ReplaceJob(ctx context.Context, cl client.Client, namespace, name string, timeout time.Duration) error {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := cl.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting Job %s/%s: %w", namespace, name, err)
	}

	key := client.ObjectKey{Namespace: namespace, Name: name}
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		return apierrors.IsNotFound(cl.Get(ctx, key, &batchv1.Job{})), nil
	})
	if err != nil {
		return fmt.Errorf("Job %s/%s was still there %s after being deleted: %w", namespace, name, timeout, err)
	}
	return nil
}
