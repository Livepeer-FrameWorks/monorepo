import { index, route } from "@react-router/dev/routes";

export default [
  index("./routes/home.jsx"),
  route("analytics", "./routes/analytics.jsx"),
  route("pricing", "./routes/pricing.jsx"),
  route("about", "./routes/about.jsx"),
  route("contact", "./routes/contact.jsx"),
  route("status", "./routes/status.jsx"),
  route("privacy", "./routes/privacy.jsx"),
  route("terms", "./routes/terms.jsx"),
  route("aup", "./routes/aup.jsx"),
  route("sitemap.xml", "./routes/sitemap.xml.js"),
];
