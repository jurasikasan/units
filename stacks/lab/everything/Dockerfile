# The MCP project's own reference server, which exercises every feature of
# MCP 2026-07-28: tools (a sum, an image, structured content, a long run),
# elicitation, resources. The lab's first tool server; pinned, from npm.
FROM node:24-alpine
RUN npm install -g @modelcontextprotocol/server-everything@2.3.0 && npm cache clean --force
ENV HOST=0.0.0.0 PORT=3001
EXPOSE 3001
USER node
CMD ["mcp-server-everything", "streamableHttp"]
