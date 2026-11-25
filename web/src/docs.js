// The API reference page. Swagger UI ships untyped bundles, so this file is plain JS.
import SwaggerUIBundle from "swagger-ui-dist/swagger-ui-es-bundle.js";
import "swagger-ui-dist/swagger-ui.css";

SwaggerUIBundle({ dom_id: "#docs", url: "/openapi.yaml" });
