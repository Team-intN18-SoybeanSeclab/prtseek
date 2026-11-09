import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "prtseek",
  version: packageJson.version,
  copyright: `© ${currentYear}, prtseek.`,
  meta: {
    title: "prtseek — 研究终端",
    description: "prtseek · 分析、研究与验证工作台",
  },
};
