import { task } from "hardhat/config";
import "@nomiclabs/hardhat-ethers";

task("stake", "Prints an account's stake amount in DioneStaking contract")
    .addParam("contract", "DioneStaking contract address")
    .addParam("account", "The account's address")
    .setAction(async (args, hre) => {
        const DioneStaking = await hre.ethers.getContractFactory("DioneStaking");
        const contract = DioneStaking.attach(args.contract);
        const a = await contract.minerStake(args.account);
        console.log("Stake amount:", a.div(hre.ethers.constants.WeiPerEther).toString());
    });

task("accounts", "Prints the list of accounts", async (args, hre) => {
    const accounts = await hre.ethers.getSigners();
      
    for (const account of accounts) {
        console.log(await account.address);
    }
});

task("oracleRequest", "Makes oracle request to Mediator contract")
    .addParam("contract", "Mediator contract address")
    .addParam("chainid", "Chain ID from which need to request info")
    .addParam("method", "Method name of requesting chain RPC")
    .addParam("param", "Value of parameter for specified method")
    .setAction(async (args, hre) => {
        const Mediator = await hre.ethers.getContractFactory("Mediator");
        const contract = Mediator.attach(args.contract);
        const res = await contract.request(parseInt(args.chainid), args.method, args.param)
        console.log("Request has successfully been sent.")
        console.log("Transaction info:")
        console.log(res)
    })