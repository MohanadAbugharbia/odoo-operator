package utils

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetSecretValue(client client.Client, ctx context.Context, namespace string, secretName string, secretKey string) (string, error) {
	secret := &corev1.Secret{}

	err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			return "", ErrSecretNotFound
		}
		return "", err
	}

	value, ok := secret.Data[secretKey]
	if !ok {
		return "", ErrSecretKeyNotFound
	}
	return string(value), nil
}

func GetInt32SecretValue(client client.Client, ctx context.Context, namespace string, secretName string, secretKey string) (int32, error) {
	valueStr, err := GetSecretValue(client, ctx, namespace, secretName, secretKey)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(valueStr, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(value), nil
}

func GetBoolSecretValue(client client.Client, ctx context.Context, namespace string, secretName string, secretKey string) (bool, error) {
	valueStr, err := GetSecretValue(client, ctx, namespace, secretName, secretKey)
	if err != nil {
		return false, err
	}
	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return false, err
	}
	return value, nil
}
