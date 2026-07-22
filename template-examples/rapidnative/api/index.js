require("dotenv").config();
const express = require("express");
const app = express();
const port = process.env.PORT || 3000;

app.use(express.json());

app.get("/", (req, res) => {
  res.json({
    project: process.env.RAPIDNATIVE_PROJECT_ID,
    environment: process.env.RAPIDNATIVE_ENV,
    message: "API server running",
  });
});

app.get("/health", (req, res) => {
  res.json({ status: "ok" });
});

app.listen(port, () => {
  console.log("[api] Running on port " + port);
});
