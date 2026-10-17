import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/task-interleaving-api",
    },
    {
      type: "category",
      label: "Policy",
      link: {
        type: "doc",
        id: "api-reference/rest/policy",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/declare-interleaving-policy",
          label: "Declare or replace a site's interleaving policy",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-interleaving-policy",
          label: "Get a site's interleaving policy",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Hints",
      link: {
        type: "doc",
        id: "api-reference/rest/hints",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-hints",
          label: "List interleaving hints",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-hint",
          label: "Get an interleaving hint",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Stations",
      link: {
        type: "doc",
        id: "api-reference/rest/stations",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-station",
          label: "Get a station's position and active hint",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Recommendations",
      link: {
        type: "doc",
        id: "api-reference/rest/recommendations",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/recommend-tasks",
          label: "Simulate the recommender for a station",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Health",
      link: {
        type: "doc",
        id: "api-reference/rest/health",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/healthz",
          label: "Liveness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/readyz",
          label: "Readiness probe",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
