# A real browser for the lab's end-to-end check; the library matches the
# browsers in the image.
FROM mcr.microsoft.com/playwright:v1.65.1-noble
WORKDIR /e2e
RUN npm init -y >/dev/null && npm install --no-audit --no-fund playwright@1.65.1
COPY chat.mjs .
ENTRYPOINT ["node", "chat.mjs"]
