package utils

import (
	"context"
	"log/slog"
	"reflect"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Resource operations
const (
	UPDATE = "Update"
	PATCH  = "Patch"
)

func UpdateK8sCRStatus(ctx context.Context, c client.Client, object client.Object) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := c.Status().Update(ctx, object)
		return err
	})

	if err != nil {
		return err
	}

	return nil
}

// CreateK8sCR creates/updates/patches an object.
func CreateK8sCR(ctx context.Context, c client.Client,
	logger *slog.Logger,
	newObject client.Object, ownerObject client.Object,
	operation string) (err error) {

	// Get the name and namespace of the object:
	key := client.ObjectKeyFromObject(newObject)
	logger.Info("[CreateK8sCR] Resource", "name", key.Name)

	// We can set the owner reference only for objects that live in the same namespace, as cross
	// namespace owners are forbidden. This also applies to non-namespaced objects like cluster
	// roles or cluster role bindings; those have empty namespaces so the equals comparison
	// should also work.
	if ownerObject != nil && ownerObject.GetNamespace() == key.Namespace {
		err = controllerutil.SetControllerReference(ownerObject, newObject, c.Scheme())
		if err != nil {
			return err
		}
	}

	// Create an empty object of the same type of the new object. We will use it to fetch the
	// current state.
	objectType := reflect.TypeOf(newObject).Elem()
	oldObject := reflect.New(objectType).Interface().(client.Object)

	// If the newObject is unstructured, we need to copy the GVK to the oldObject.
	if unstructuredObj, ok := newObject.(*unstructured.Unstructured); ok {
		oldUnstructuredObj := oldObject.(*unstructured.Unstructured)
		oldUnstructuredObj.SetGroupVersionKind(unstructuredObj.GroupVersionKind())
	}

	err = c.Get(ctx, key, oldObject)

	// If there was an error obtaining the CR and the error was "Not found", create the object.
	// If any other other occurred, return the error.
	// If the CR already exists, patch it or update it.
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info(
				"[CreateK8sCR] CR not found, CREATE it",
				"name", newObject.GetName(),
				"namespace", newObject.GetNamespace())
			err = c.Create(ctx, newObject)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		newObject.SetResourceVersion(oldObject.GetResourceVersion())
		if operation == PATCH {
			logger.Info("[CreateK8sCR] CR already present, PATCH it",
				"name", newObject.GetName(),
				"namespace", newObject.GetNamespace())
			return c.Patch(ctx, newObject, client.MergeFrom(oldObject))
		} else if operation == UPDATE {
			logger.Info("[CreateK8sCR] CR already present, UPDATE it",
				"name", newObject.GetName(),
				"namespace", newObject.GetNamespace())
			return c.Update(ctx, newObject)
		}
	}

	return nil
}

func DoesK8SResourceExist(ctx context.Context, c client.Client,
	logger *slog.Logger,
	name, namespace string, obj client.Object) (resourceExists bool, err error) {
	err = c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("[doesK8SResourceExist] Resource not found, create it. ",
				"name", name, "namespace", namespace)
			return false, nil
		} else {
			return false, err
		}
	} else {
		logger.Info("[doesK8SResourceExist] Resource already present, return. ",
			"name", name, "namespace", namespace)
		return true, nil
	}
}
