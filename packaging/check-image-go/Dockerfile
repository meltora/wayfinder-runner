FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0

ENV GOTOOLCHAIN=local \
    GOPATH=/home/runner/go \
    GOTMPDIR=/home/runner \
    GOFLAGS=-modcacherw

RUN groupadd --gid 10001 runner \
    && useradd --create-home --uid 10001 --gid 10001 runner \
    && find / -xdev -type f -perm /6000 -exec chmod a-s {} + \
    && test -z "$(find / -xdev -type f -perm /6000)" \
    && test "$(id -u runner):$(id -g runner)" = "10001:10001" \
    && go version | grep -Eq '^go version go1\.27\.3 linux/(amd64|arm64)$'

USER runner
WORKDIR /home/runner
