# Example image: layers a demo skill + CLAUDE.md onto the neutral base, proving
# the build-time /workspace injection seam. Build the base first:
#   docker build -t agentkit-sandbox:dev agent-library/sandbox
#   docker build -t agentkit-example:dev agent-library/go/examples/exampleimage
FROM agentkit-sandbox:dev
COPY workspace/ /workspace/
