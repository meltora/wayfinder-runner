FROM node:22-bookworm-slim@sha256:f32b81066cde10a75dbac96646099533316d94bac4150c55da1636e1f0ffdc46

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates bubblewrap socat \
    && find / -xdev -type f -perm /6000 ! -path /usr/bin/bwrap -exec chmod a-s {} + \
    && chmod u+s /usr/bin/bwrap \
    && rm -rf /var/lib/apt/lists/*

# npm 12.0.2 still bundles four older transitive packages. The build replaces
# their installed copies with separately locked fixed releases. The source
# lockfile is deliberately removed from the runtime layer afterward because it
# describes npm's upstream bundle before those replacements.
COPY packaging/job-image/package.json packaging/job-image/package-lock.json /opt/agent-tools/
RUN npm ci --omit=dev --prefix /opt/agent-tools \
    && node /opt/agent-tools/node_modules/@anthropic-ai/claude-code/install.cjs \
    && rm -rf /usr/local/lib/node_modules/npm \
              /opt/agent-tools/node_modules/npm/node_modules/brace-expansion \
              /opt/agent-tools/node_modules/npm/node_modules/ip-address \
              /opt/agent-tools/node_modules/npm/node_modules/tar \
              /opt/agent-tools/node_modules/npm/node_modules/undici \
    && rm -f /usr/local/bin/npm /usr/local/bin/npx \
    && cp -a /opt/agent-tools/node_modules/brace-expansion \
             /opt/agent-tools/node_modules/npm/node_modules/brace-expansion \
    && cp -a /opt/agent-tools/node_modules/ip-address \
             /opt/agent-tools/node_modules/npm/node_modules/ip-address \
    && cp -a /opt/agent-tools/node_modules/tar \
             /opt/agent-tools/node_modules/npm/node_modules/tar \
    && cp -a /opt/agent-tools/node_modules/undici \
             /opt/agent-tools/node_modules/npm/node_modules/undici \
    && ln -s /opt/agent-tools/node_modules/.bin/npm /usr/local/bin/npm \
    && ln -s /opt/agent-tools/node_modules/.bin/npx /usr/local/bin/npx \
    && ln -s /opt/agent-tools/node_modules/.bin/claude /usr/local/bin/claude \
    && ln -s /opt/agent-tools/node_modules/.bin/codex /usr/local/bin/codex \
	&& ln -s /opt/agent-tools/node_modules/.bin/opencode /usr/local/bin/opencode \
    && ln -s /opt/agent-tools/node_modules/.bin/srt /usr/local/bin/srt \
    && test "$(node -p "require('/opt/agent-tools/node_modules/npm/node_modules/brace-expansion/package.json').version")" = "5.0.9" \
    && test "$(node -p "require('/opt/agent-tools/node_modules/npm/node_modules/ip-address/package.json').version")" = "10.3.1" \
    && test "$(node -p "require('/opt/agent-tools/node_modules/npm/node_modules/tar/package.json').version")" = "7.5.22" \
    && test "$(node -p "require('/opt/agent-tools/node_modules/npm/node_modules/undici/package.json').version")" = "6.28.0" \
    && claude --version \
    && codex --version \
	&& opencode --version \
    && srt --version \
    && npm cache clean --force \
    && rm -f /opt/agent-tools/package-lock.json

RUN useradd --create-home --uid 10001 runner
COPY --chown=root:root --chmod=0755 bin/our-runner-linux /usr/local/bin/our-runner

USER runner
WORKDIR /home/runner
