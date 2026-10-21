ObjectBucket Notifications Adapter
==================================

The adapter will be an HTTP server and/or Kafka consumer running in a pod inside a k8s cluster, configured in noobaa/radosgw as "bucketNotifications" connection (either as HTTP notifications, or Kafka notifications)
It will distribute the notifications received from NooBaa or radosgw to individual triggers, according to the configuration specified by ObjectBucketSource custom resources.
The notifications to individual triggers will be sent as cloudevents via HTTP or Kafka
It will be implemented in go, using k8s client libraries, the cloudevents k8s libraries, aws s3 client library, https://github.com/IBM/sarama, and the operator sdk (https://sdk.operatorframework.io)

NooBaa HTTP connection setup (manual, one-time)
-----------------------------------------------

Before the adapter can receive bucket notifications via HTTP, it must be registered as an
HTTP notification connection in NooBaa/radosgw. This is a manual, one-time setup step
performed by a cluster administrator. Example for Noobaa:

1. Create a Secret in the openshift-storage namespace containing the connection
   details for the adapter. The Secret holds a JSON file describing the HTTP
   endpoint:

```
oc create secret generic mcg-adapter-connection \
  --from-file=connect.json=/dev/stdin -n openshift-storage <<EOF
{
  "name": "mcg-adapter-connection",
  "notification_protocol": "http",
  "agent_request_object": {
    "host": "<adapter-service>.<adapter-namespace>.svc.cluster.local",
    "port": <adapter-port>
  }
}
EOF
```

2. Patch the NooBaa CR to register the connection and enable bucket
   notifications:

```
existing_connections=$(oc get noobaa noobaa -n openshift-storage -o json \
  | jq -c '.spec.bucketNotifications.connections // []')

updated_connections=$(echo "$existing_connections" | jq -c \
  --arg name "mcg-adapter-connection" \
  '[.[] | select(.name != $name)] + [{"name": $name, "namespace": "openshift-storage"}]')

oc patch noobaa noobaa --type='merge' -n openshift-storage -p '{
  "spec": {
    "bucketNotifications": {
      "connections": '"${updated_connections}"',
      "enabled": true
    }
  }
}'
```

The adapter's deployment should be configured with environment variables that
identify its connection name (NOOBAA_ADAPTER_ID and NOOBAA_ADAPTER_TOPIC_ARN). When the operator
calls put-bucket-notification for a bucket, it uses these values so that NooBaa
knows where to deliver notifications (HTTP endpoint or Kafka topic, depending on
NOTIFICATIONS_MODE).

ObjectBucketSource CRD
----------------------------

an example CR from the ObjectBucketSource:
```
apiVersion: sources.functions.dev/v1alpha1
kind: ObjectBucketSource
metadata:
  namespace: foobar
  name: foo
spec:
  objectBucketClaim:
    name: foo-bucket
  events:
  - "s3:ObjectCreated:*"
  sink:
    uri: http://foo.foobar.svc.cluster.local
status:
  conditions:
  - type: OBCCredentialsAvailable
    status: "True"
  - type: BucketNotificationSet
    status: "True"
  - type: TestEventReceived
    status: "True"
```

As an alternative, uri can point to a Kafka Topic:
```
apiVersion: sources.functions.dev/v1alpha1
kind: ObjectBucketSource
metadata:
  namespace: foobar
  name: foo
spec:
  objectBucketClaim:
    name: foo-bucket
  events:
  - "s3:ObjectCreated:*"
  sink:
    uri: kafka:foo-topic
status:
   ...
```

Where "objectBucketClaim" is a reference to an ObjectBucketClaim (living in the same namespace as the ObjectBucketSource), e.g.:


```
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  creationTimestamp: "2026-06-17T09:16:31Z"
  finalizers:
  - objectbucket.io/finalizer
  generation: 2
  labels:
    app: noobaa
    bucket-provisioner: openshift-storage.noobaa.io-obc
    noobaa-domain: openshift-storage.noobaa.io
  name: foo-bucket
  namespace: foobar
  resourceVersion: "1002740"
  uid: edbcd3af-3ffa-4f66-8167-2d2ee24834b7
spec:
  additionalConfig:
    bucketclass: noobaa-default-bucket-class
  bucketName: foo-bucket
  objectBucketName: obc-foobar-foo-bucket
  storageClassName: openshift-storage.noobaa.io
status:
  phase: Bound
```

the triggers.uri is, for now an, absolute URL where to send the cloudevents to.
the trigger can also identify a Kafka topic to send the event to. The Kafka server to connect to will be defined by global default (configured via adapter env).

the status conditions are:

* OBCCredentialsAvailable:

  Set to "True" once the adapter has found the ConfigMap and Secret corresponding
  to the referenced ObjectBucketClaim (same namespace, same name as the OBC) and
  verified that they contain the expected keys (BUCKET_HOST, BUCKET_NAME,
  BUCKET_PORT in the ConfigMap; AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY in the
  Secret). If the ConfigMap or Secret does not exist yet, or is missing required
  keys, the condition remains "False" and the operator requeues the
  reconciliation until they become available.

* BucketNotificationSet:
  
  The adapter will read the configmap and secret that corresponds to the ObjectBucketClaim (in the same namespace, with the same name as the OBC name)
e.g. 

```
apiVersion: v1
kind: ConfigMap
metadata:
  name: foo-bucket
data:
  BUCKET_HOST: s3.openshift-storage.svc
  BUCKET_NAME: foo-bucket
  BUCKET_PORT: "443"
  BUCKET_REGION: ""
  BUCKET_SUBREGION: ""
```

and

```
apiVersion: v1
kind: Secret
metadata:
  name: foo-bucket
data:
  AWS_ACCESS_KEY_ID: ...
  AWS_SECRET_ACCESS_KEY: ...
```

and use those connection details and keys to issue the request to noobaa using S3-compatible API, equivalent to:
```
aws s3api put-bucket-notification --bucket "foo-bucket" --notification-configuration '{
  "TopicConfiguration": {
    "Id": "$NOOBAA_ADAPTER_ID",
    "Events": ["s3:ObjectCreated:*"],
    "Topic": "$NOOBAA_ADAPTER_TOPIC_ARN"
  }
}'
```

The Topic in the S3 notification configuration is always a reference to the
NooBaa connection secret (NOOBAA_ADAPTER_TOPIC_ARN, e.g. "mcg-adapter-connection/connect.json").
It is not the Kafka topic itself — for Kafka mode, the actual Kafka topic is the
"topic" field inside the connect.json (see the NooBaa Kafka connection setup
section). For HTTP mode, the connect.json contains the HTTP endpoint details
instead.

If successful, the BucketNotificationSet condition will be marked as "True"

* TestEventReceived will be set to "True", once the adapter receives the TestEvent

The noobaa bucket notification sends a test event to the newly configured bucket notifications, e.g.:

```
{"Records":[{"Service":"NooBaa","Event":"s3:TestEvent","Time":"2026-06-17T13:59:25.679Z","Bucket":"foo-bucket","RequestId":"mqi50jq0-bf2pdy-pbh","HostId":"ip-10-0-38-44.us-west-2.compute.internal"}]}
```

The test event arrives via the adapter's HTTP server (in HTTP mode) or on the
Kafka notifications topic (in Kafka mode). Once the adapter receives it through
either path, it will set the TestEventReceived condition to True

Event dispatch
--------------

The noobaa event may look like this:

```
{"Records":[{"eventVersion":"2.3","eventSource":"noobaa:s3","eventTime":"2026-06-17T11:28:44.771Z","s3":{"s3SchemaVersion":"1.0","object":{"sequencer":2,"key":"uploads/2026/06/17/6b8d9929-b142-4138-bb30-49487f6a0377-me_hat.jpg","size":1958176,"eTag":"2a198f942024fc2854fb70830c0988c3"},"bucket":{"name":"foo-bucket","ownerIdentity":{"principalId":"operator@noobaa.io"},"arn":"arn:aws:s3:::foo-bucket"}},"eventName":"ObjectCreated:Put","userIdentity":{"principalId":"obc-account.foo-bucket.6a3265ef@noobaa.io"},"requestParameters":{"sourceIPAddress":"::ffff:10.131.0.48"},"responseElements":{"x-amz-request-id":"mqhzmrdr-5uhto0-6s","x-amz-id-2":"mqhzmrdr-5uhto0-6s"}}]}
```

for each record, the dispatcher will match the bucket name and eventName and find the list of triggers matching both the bucket name and the event name.
(dropping the "s3:" prefix, and matching '*' at the end of the specified event name as meaning any suffix (like in this case, "ObjectCreated:Put" matching the specified "s3:ObjectCreated:*")
To each such trigger, it will send the list of records as a cloudevent (it will keep the same JSON structure of the original, including thje Records list, but will filter out records not matching the rules.)

Multiple ObjectBucketSource resources for the same bucket
----------------------------------------------------

When multiple ObjectBucketSource CRs reference the same OBC (same bucket), the
operator merges them internally into a single S3 bucket notification
configuration. The merged configuration subscribes to the superset (union) of
all event types requested across all ObjectBucketSource CRs for that bucket.

For example, if trigger A subscribes to "s3:ObjectCreated:*" and trigger B
subscribes to "s3:ObjectRemoved:*", the operator issues a single
put-bucket-notification call with Events: ["s3:ObjectCreated:*",
"s3:ObjectRemoved:*"].

When the adapter receives a notification from NooBaa, it filters the event
internally before dispatching: each trigger URI only receives events matching
the event types specified in its own ObjectBucketSource CR. A trigger that subscribed
only to "s3:ObjectCreated:*" will never receive "s3:ObjectRemoved:*" events,
even though the bucket-level notification is configured for both.

When a ObjectBucketSource CR is deleted or updated, the operator recomputes the
merged event set for the affected bucket. If the superset shrinks (i.e., no
remaining CR needs a previously included event type), the operator updates the
S3 bucket notification configuration accordingly. If no ObjectBucketSource CRs
remain for a bucket, the operator removes the bucket notification entirely


Finalizer and cleanup on deletion
----------------------------------

The operator adds a finalizer (e.g., "sources.functions.dev/objectbucketsource") to
each ObjectBucketSource CR on creation. When a ObjectBucketSource CR is deleted, the
finalizer ensures the operator has a chance to clean up before the resource is
removed from the API server. During finalization, the operator recomputes the
merged event set for the affected bucket (excluding the CR being deleted). If
other ObjectBucketSource CRs still reference the same bucket, the operator updates
the S3 bucket notification configuration with the reduced event set. If no
ObjectBucketSource CRs remain for the bucket, the operator removes the bucket
notification from NooBaa entirely. Once cleanup is complete, the operator removes
the finalizer, allowing Kubernetes to garbage-collect the CR.


RBAC
----

The adapter runs under a dedicated ServiceAccount. A ClusterRole grants the
following permissions, bound via a ClusterRoleBinding:

- ConfigMaps and Secrets: cluster-wide read access (get, list, watch). This is
  required because ObjectBucketSource CRs can exist in any namespace and the adapter
  needs to read the OBC-associated ConfigMap and Secret from each CR's namespace.

- ObjectBucketSource CRs (sources.functions.dev/v1alpha1): full access (get, list,
  watch, create, update, patch, delete). The operator needs to watch for changes,
  update status conditions, and manage finalizers.

NooBaa Kafka connection setup (manual, one-time)
-------------------------------------------------

As an alternative to the HTTP connection, the adapter can receive bucket
notifications from NooBaa via a Kafka topic. This requires a Strimzi-managed
Kafka cluster and is a manual, one-time setup performed by a cluster
administrator.

1. Create the Kafka topic and users (Strimzi):

```
# Topic for NooBaa notifications
cat <<EOF | oc apply -f -
apiVersion: kafka.strimzi.io/v1
kind: KafkaTopic
metadata:
  name: mcg-adapter-notifications
  namespace: kafka
  labels:
    strimzi.io/cluster: my-cluster
spec:
  partitions: 8
  replicas: 3
  config:
    retention.ms: 86400000
    cleanup.policy: delete
EOF

# KafkaUser for NooBaa to publish notifications
cat <<EOF | oc apply -f -
apiVersion: kafka.strimzi.io/v1
kind: KafkaUser
metadata:
  name: noobaa-notifications-user
  namespace: kafka
  labels:
    strimzi.io/cluster: my-cluster
spec:
  authentication:
    type: scram-sha-512
  authorization:
    type: simple
    acls:
      - resource:
          type: topic
          name: mcg-adapter-notifications
          patternType: literal
        operations: [Read, Describe, Write, Create, Delete]
        host: "*"
      - resource:
          type: group
          name: mcg-adapter-notifications
          patternType: literal
        operations: [Describe]
        host: "*"
EOF

# KafkaUser for the adapter (consuming notifications + producing cloudevents)
cat <<EOF | oc apply -f -
apiVersion: kafka.strimzi.io/v1
kind: KafkaUser
metadata:
  name: mcg-adapter-user
  namespace: kafka
  labels:
    strimzi.io/cluster: my-cluster
spec:
  authentication:
    type: scram-sha-512
  authorization:
    type: simple
    acls:
      - resource:
          type: topic
          name: "*"
        operations: [Read, Describe, Write, Create, Delete]
        host: "*"
      - resource:
          type: group
          name: "*"
        operations: [Read]
        host: "*"
EOF
```

2. Create a Secret in the openshift-storage namespace containing the Kafka
   connection details for NooBaa, including SASL credentials from the
   noobaa-notifications-user KafkaUser secret:

```
password=$(oc get secret -n kafka noobaa-notifications-user \
  -o jsonpath='{.data.password}' | base64 --decode)

name="mcg-adapter-connection"

tmp=$(mktemp -d)
cat > "$tmp/connect.json" <<EOF
{
  "name": "$name",
  "notification_protocol": "kafka",
  "topic": "mcg-adapter-notifications",
  "kafka_options_object": {
    "metadata.broker.list": "my-cluster-kafka-bootstrap.kafka.svc:9095",
    "security.protocol": "SASL_PLAINTEXT",
    "sasl.mechanism": "SCRAM-SHA-512",
    "sasl.username": "noobaa-notifications-user",
    "sasl.password": "$password"
  }
}
EOF

oc delete secret -n openshift-storage "$name" || true
oc create secret generic "$name" \
  --from-file="$tmp/connect.json" -n openshift-storage
rm -rf "$tmp"
```

3. Patch the NooBaa CR to register the Kafka connection and enable bucket
   notifications:

```
existing_connections=$(oc get noobaa noobaa -n openshift-storage -o json \
  | jq -c '.spec.bucketNotifications.connections // []')

name="mcg-adapter-connection"
updated_connections=$(echo "$existing_connections" | jq -c \
  --arg name "$name" \
  '[.[] | select(.name != $name)] + [{"name": $name, "namespace": "openshift-storage"}]')

oc patch noobaa noobaa --type='merge' -n openshift-storage -p '{
  "spec": {
    "bucketNotifications": {
      "connections": '"${updated_connections}"',
      "enabled": true
    }
  }
}'
```

4. When the operator calls put-bucket-notification for a bucket, it uses
   NOOBAA_ADAPTER_ID and NOOBAA_ADAPTER_TOPIC_ARN, e.g.:

```
aws s3api put-bucket-notification --bucket "foo-bucket" \
  --notification-configuration '{
    "TopicConfiguration": {
      "Id": "mcg-adapter",
      "Events": ["s3:ObjectCreated:*"],
      "Topic": "mcg-adapter-connection/connect.json"
    }
  }'
```


Configuration
-------------------

Configuration is defined via env on the adapter Deployment:

	NOOBAA_ADAPTER_ID: identifier for NooBaa bucket notification configuration
	    (default: "mcg-adapter")
	NOOBAA_ADAPTER_TOPIC_ARN: NooBaa connection secret reference used as TopicArn in
	    put-bucket-notification calls
	    (default: "mcg-adapter-connection/connect.json")
	NOOBAA_ADAPTER_STORAGECLASS_PATTERN: A regular expression on OBC
	    .spec.storageClassName to classify the OBC as a NooBaa-managed OBC
	    (default: ".*noobaa\.io$")
	RADOSGW_ADAPTER_ID: identifier for RadosGW bucket notification configuration
	    (default: "rgw-adapter")
	RADOSGW_ADAPTER_TOPIC_ARN: RadosGW TopicArn in put-bucket-notification calls
	    (default: "arn:aws:sns:ocs-storagecluster-cephobjectstore::rgw-adapter-notifications")
	RADOSGW_ADAPTER_STORAGECLASS_PATTERN: A regular expression on OBC
	    .spec.storageClassName to classify the OBC as a ceph/radosgw-managed OBC
	    (default: ".*ceph-rgw$")
	NOTIFICATIONS_MODE: "http" or "kafka" — selects whether the adapter
	    receives NooBaa/RadosGW bucket notifications via its HTTP server or by
	    consuming from a Kafka topic (default: "http")
	KAFKA_BROKERS: comma-separated list of host:port (required in both
	    modes — even in HTTP mode the adapter needs Kafka to produce
	    cloudevents to trigger Kafka topics)
	KAFKA_SECRET: <name of the secret from the namespace of the adapter
	    deployment> (required in both modes, same reason as KAFKA_BROKERS)
	KAFKA_NOTIFICATIONS_TOPIC: comma-separated list of the Kafka topics to
	    consume NooBaa/RadosGW bucket notifications from
	    (e.g. "mcg-adapter-notifications,rgw-adapter-notifications").
	    Only required when NOTIFICATIONS_MODE=kafka.
	KAFKA_NOTIFICATIONS_GROUP_ID: the consumer group ID for consuming
	    NooBaa/RadosGW notifications (e.g. "objectbucket-notifications-adapter").
	    Only required when NOTIFICATIONS_MODE=kafka.

The same Kafka credentials (KAFKA_SECRET) are used for both consuming
notifications from NooBaa/RadosGW and producing cloudevents to trigger Kafka topics.

The format of the kafka secret is defined in ./kafka-secret-format.md

Delivery failure handling (non-goal)
------------------------------------

Retry policies, dead-lettering, and delivery guarantees for trigger URIs are
deferred for a later iteration. In this initial version, if a trigger URI is
unreachable or returns an error, the adapter logs the error and moves on. No
retries are attempted and the event is dropped

