# Running raft-kv on AWS

Two paths. Pick based on whether you want to learn **infrastructure** (EC2) or
**orchestration** (EKS). Both cost money — tear them down when you're done.

> Ports: client API `8080/tcp`, peer RPC `9000/tcp`. Peers talk over `9000`,
> clients hit `8080`.

---

## Option A — three EC2 instances (closest to "real machines")

This is the only setup that proves the property local/Docker can't: surviving a
**whole machine** dying.

1. **Launch 3 instances** (t3.micro is plenty) in the same VPC, ideally across
   3 availability zones so an AZ outage only takes one node.

2. **Security group** — create one SG and attach it to all three:
   - Inbound `9000/tcp` from **the SG itself** (peer replication, nodes only).
   - Inbound `8080/tcp` from **the SG itself** and from wherever your clients
     live (e.g. your office IP). Do **not** open `8080` to `0.0.0.0/0` — there
     is no auth (see `docs/threat-model.md`).
   - Inbound `22/tcp` from your IP for SSH.

3. **Build and copy the binary** to each instance (Amazon Linux / Ubuntu are
   x86-64; adjust `GOARCH` for Graviton/arm64):

   ```bash
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o raft-kv ./cmd/server
   scp raft-kv ec2-user@<public-ip>:~
   ```

4. **Install the systemd unit** on each node (files in this directory):

   ```bash
   sudo cp raft-kv /usr/local/bin/raft-kv
   sudo cp raft-kv.service /etc/systemd/system/raft-kv.service
   sudo cp raft-kv.env.example /etc/raft-kv.env   # EDIT per node
   sudo useradd --system --home /var/lib/raft-kv --create-home raftkv || true
   sudo systemctl daemon-reload
   sudo systemctl enable --now raft-kv
   ```

   Edit `/etc/raft-kv.env` on each box so `RAFT_ID` and `RAFT_PEERS` use the
   instances' **private IPs** (nodes replicate over the VPC).

5. **Use it** from a client that can reach `8080`:

   ```bash
   printf 'PUT foo bar\n' | nc <some-node-private-ip> 8080
   # follow any "NOT_LEADER <ip:8080>" reply to the leader
   ```

6. **Prove fault tolerance:** `sudo systemctl stop raft-kv` on the leader (or
   stop the instance). A new leader is elected within a couple of seconds and
   committed data survives. Bring it back and it rejoins by replaying `/var/lib/raft-kv`.

**Teardown:** terminate the 3 instances and delete the security group.

---

## Option B — EKS (reuse the Kubernetes manifests)

The manifests in `../k8s/raft-kv.yaml` run as-is on EKS; only two things change
versus a local kind cluster: the **image registry** and the **storage class**.

1. **Cluster + node group** (eksctl is simplest):

   ```bash
   eksctl create cluster --name raft-kv --nodes 3 --node-type t3.small
   ```

2. **Push the image to ECR:**

   ```bash
   ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
   REGION=$(aws configure get region)
   REPO=$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/raft-kv
   aws ecr create-repository --repository-name raft-kv || true
   aws ecr get-login-password | docker login --username AWS --password-stdin $ACCOUNT.dkr.ecr.$REGION.amazonaws.com
   docker build -t $REPO:v1 .
   docker push $REPO:v1
   ```

   Then set `image: <that ECR URL>:v1` in `../k8s/raft-kv.yaml`.

3. **Storage:** EKS provisions EBS via the EBS CSI driver. Either install the
   `gp3` StorageClass and uncomment `storageClassName: gp3` in the
   volumeClaimTemplate, or rely on the cluster default.

4. **Apply and expose:**

   ```bash
   kubectl apply -f ../k8s/raft-kv.yaml
   kubectl rollout status statefulset/raft-kv
   ```

   For external client access, change `raft-kv-client` to
   `type: LoadBalancer` (provisions an NLB). Remember the leader-redirect
   caveat in `../README.md` — the hint is an in-cluster DNS name.

**Teardown:** `eksctl delete cluster --name raft-kv` (and delete the ECR repo).
